package content

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/processing"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/ranking"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/elastic"
	idx "github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/items"
	"io"
	"sort"
	"unicode/utf8"
)

func split(key string) bool {
	digest := sha256.Sum256([]byte(key))
	return binary.BigEndian.Uint32(digest[:4])%5 == 0
}

// BuildSamples mines actual retrieval negatives and obtains source-grounded teacher labels.
func (s *Service) BuildSamples(ctx context.Context, out io.Writer, embedding bool, scenario item.Scenario) error {
	if scenario.RRF < 1 || scenario.CandidateLimit < 1 || len(scenario.Kinds) == 0 {
		return item.ErrInvalid
	}
	teacher, ok := s.analyzer.(processing.Teacher)
	if !ok {
		return fmt.Errorf("teacher relevance model unavailable")
	}
	retrieval := s.config.Retrieval
	writer := json.NewEncoder(out)
	after := ""
	for {
		rows, e := s.store.Samples(ctx, after, 100)
		if e != nil {
			return e
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			after = row.ID
			var seed item.Sample
			if e = json.Unmarshal([]byte(row.Payload), &seed); e != nil {
				return e
			}
			enc, e := s.encoder.Encode(ctx, retrieval.Model, retrieval.Space, []string{seed.Query})
			if e != nil {
				return e
			}
			hits := map[string]*item.Hit{}
			appendCandidates := func(lane string, candidates []idx.Candidate, highlights map[string]string) error {
				ids := make([]string, 0, len(candidates))
				for _, candidate := range candidates {
					ids = append(ids, candidate.ItemID)
				}
				valid, err := s.Resolve(ctx, ids, scenario.Kinds)
				if err != nil {
					return err
				}
				for rank, candidate := range candidates {
					value, ok := valid[candidate.ItemID]
					if !ok || value.RevisionID != candidate.RevisionID {
						continue
					}
					if lane == "elastic" && highlights[value.ID] != "" {
						loc, _, err := s.FindExcerpt(ctx, candidate.Location, highlights[value.ID], 4096)
						if err != nil {
							continue
						}
						candidate.Location = loc
					}
					hit := hits[value.ID]
					if hit == nil {
						value.Location = candidate.Location
						hit = &item.Hit{Item: value, Scores: map[string]float64{}}
						hits[value.ID] = hit
					}
					hit.Scores[lane] = candidate.Score
					hit.Scores["rrf"] += 1 / float64(scenario.RRF+rank+1)
				}
				return nil
			}
			if scenario.Delivery == "search" && s.lexical != nil && s.graph != nil {
				found, err := s.lexical.Search(ctx, elastic.Query{Text: seed.Query, Kinds: scenario.Kinds, Limit: scenario.CandidateLimit})
				if err != nil {
					return err
				}
				candidates := make([]idx.Candidate, 0, len(found))
				highlights := map[string]string{}
				seeds := []string{seed.Query}
				for _, hit := range found {
					candidates = append(candidates, idx.Candidate{ItemID: hit.ItemID, RevisionID: hit.RevisionID, Location: item.Location{RevisionID: hit.RevisionID, SectionID: hit.SectionID, Start: hit.Start, End: hit.End}, Score: hit.Score})
					highlights[hit.ItemID] = hit.Highlight
					if hit.Title != "" {
						seeds = append(seeds, hit.Title)
					}
				}
				if err := appendCandidates("elastic", candidates, highlights); err != nil {
					return err
				}
				paths, err := s.graph.Search(ctx, seeds, scenario.Kinds, 2, scenario.CandidateLimit)
				if err != nil {
					return err
				}
				candidates = nil
				for _, path := range paths {
					candidates = append(candidates, idx.Candidate{ItemID: path.ItemID, RevisionID: path.RevisionID, Location: path.Location, Score: 1 / float64(max(path.Hops, 1))})
				}
				if err := appendCandidates("graph", candidates, nil); err != nil {
					return err
				}
			}
			for _, lane := range []string{"dense", "sparse", "multi"} {
				candidates, err := s.index.Search(ctx, retrieval, lane, enc[0], scenario.Kinds, scenario.CandidateLimit)
				if err != nil {
					return err
				}
				if err := appendCandidates(lane, candidates, nil); err != nil {
					return err
				}
			}
			ordered := []*item.Hit{}
			for _, h := range hits {
				ordered = append(ordered, h)
			}
			sort.Slice(ordered, func(i, j int) bool { return ordered[i].Scores["rrf"] > ordered[j].Scores["rrf"] })
			if len(ordered) > 20 {
				ordered = ordered[:20]
			}
			inputs := []processing.JudgmentInput{{ID: "positive", Text: seed.Positive}}
			texts := map[string]string{"positive": seed.Positive}
			for _, h := range ordered {
				if split(h.Item.DuplicateID) != split(seed.DuplicateID) {
					continue
				}
				loc := h.Item.Location
				loc.End = min(loc.End, loc.Start+1024)
				text, e := s.Read(ctx, loc, 4096)
				if e != nil {
					return e
				}
				inputs = append(inputs, processing.JudgmentInput{ID: h.Item.ID, Text: text})
				texts[h.Item.ID] = text
			}
			judgments, e := teacher.Judge(ctx, seed.Query, inputs)
			if e != nil {
				return e
			}
			grades := map[string]int{}
			for _, v := range judgments {
				grades[v.ID] = v.Grade
			}
			if embedding {
				negatives := []string{}
				for _, v := range inputs {
					if v.ID != "positive" && grades[v.ID] == 0 && utf8.RuneCountInString(v.Text) > 0 {
						negatives = append(negatives, v.Text)
					}
				}
				if grades["positive"] < 2 || len(negatives) == 0 {
					continue
				}
				seed.Negatives = negatives
				seed.LabelSource = "synthetic_teacher"
				if e = writer.Encode(seed); e != nil {
					return e
				}
				continue
			}
			for _, h := range ordered {
				grade, ok := grades[h.Item.ID]
				if !ok {
					continue
				}
				sample := seed
				sample.CandidateID = h.Item.ID
				sample.CandidateGroup = h.Item.DuplicateID
				sample.LabelSource = "synthetic_teacher"
				if scenario.Delivery == "search" {
					sample.Features = ranking.SearchFeatures(*h, nil)
				} else {
					sample.Features = ranking.Features(*h, nil)
				}
				sample.Label = float64(grade)
				if e = writer.Encode(sample); e != nil {
					return e
				}
			}
		}
	}
}
