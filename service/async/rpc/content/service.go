// Package content exposes the Async-owned whole-item lifecycle to API and Worker.
package content

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/catalog"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/processing"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/blob"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/inference"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/elastic"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/graph"
	idx "github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/items"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"io"
	"log/slog"

	"strings"
	"time"
	"unicode/utf8"
)

type Config struct {
	Retrieval              item.Retrieval
	UnderstandingModel     string
	Lexical                *elastic.Client
	Graph                  *graph.Client
	MaxLexicalSectionBytes int64
	ItemPartitions         int
	MaxUploadBytes         int64
	MaxUncompressedBytes   int64
	WindowRunes            int
	LeaseSeconds           int
	MaxAttempts            int
}
type Service struct {
	observed *telemetry.Bundle
	store    *catalog.Store
	objects  blob.Store
	encoder  inference.Encoder
	index    idx.Index
	lexical  *elastic.Client
	graph    *graph.Client
	analyzer processing.Analyzer
	config   Config
}

func New(db *gorm.DB, objects blob.Store, encoder inference.Encoder, index idx.Index, analyzer processing.Analyzer, c Config) (*Service, error) {
	if db == nil || objects == nil || c.MaxUploadBytes < 1 || c.MaxUncompressedBytes < 1 || c.WindowRunes < 256 || c.LeaseSeconds < 1 || c.MaxAttempts < 1 {
		return nil, item.ErrInvalid
	}
	return &Service{store: newCatalog(db, c.ItemPartitions), objects: objects, encoder: encoder, index: index, lexical: c.Lexical, graph: c.Graph, analyzer: analyzer, config: c}, nil
}
func (s *Service) Migrate(ctx context.Context) error { return s.store.Migrate(ctx) }
func (s *Service) Retrieval() item.Retrieval         { return s.config.Retrieval }
func (s *Service) Accept(ctx context.Context, e item.ContentEvent, body io.Reader) (item.Admission, error) {
	if e.Kind == "" && e.Operation != "delete" {
		e.Kind = "article"
	}
	if e.Operation != "delete" && e.Kind != "book" && e.Kind != "article" {
		return item.Admission{}, item.ErrInvalid
	}
	if e.Operation == "upsert" && e.MediaType == "" {
		return item.Admission{}, item.ErrInvalid
	}
	key := ""
	size := int64(0)
	hash := sha256.New()
	if e.Operation == "upsert" {
		if body == nil {
			if e.ObjectRef != "" {
				r, err := s.objects.Open(ctx, e.ObjectRef, 0, -1)
				if err != nil {
					return item.Admission{}, err
				}
				defer r.Close()
				body = r
			} else {
				body = strings.NewReader(e.Body)
			}
		}
		key = "originals/" + uuid.NewString()
		limited := io.LimitReader(body, s.config.MaxUploadBytes+1)
		var err error
		size, err = s.objects.Put(ctx, key, io.TeeReader(limited, hash), -1, e.MediaType)
		if err != nil {
			return item.Admission{}, err
		}
		if size > s.config.MaxUploadBytes {
			return item.Admission{}, fmt.Errorf("%w: upload exceeds configured limit", item.ErrInvalid)
		}
	}
	// One fingerprint at the input/commit boundary detects conflicting immutable revisions.
	semantic := struct {
		Producer, SourceID                         string
		Revision                                   int64
		Operation, URL, Media, Kind, Title, Digest string
	}{e.Producer, e.SourceID, e.Revision, e.Operation, e.SourceURL, e.MediaType, e.Kind, e.Title, hex.EncodeToString(hash.Sum(nil))}
	data, _ := json.Marshal(semantic)
	sum := sha256.Sum256(data)
	return s.store.Accept(ctx, e, key, hex.EncodeToString(sum[:]), size)
}
func (s *Service) Item(ctx context.Context, id string) (item.Item, error) {
	v, e := s.store.Item(ctx, id)
	if e != nil {
		return v, e
	}
	v.Relations, e = s.store.Relations(ctx, id, v.RevisionID, 100)
	return v, e
}
func (s *Service) Structure(ctx context.Context, id string, after, limit int) (string, []item.Section, error) {
	v, e := s.store.Item(ctx, id)
	if e != nil {
		return "", nil, e
	}
	out, e := s.store.Sections(ctx, v.RevisionID, after, limit)
	return v.RevisionID, out, e
}
func (s *Service) Job(ctx context.Context, id string) (item.Job, error) { return s.store.Job(ctx, id) }
func (s *Service) Revision(ctx context.Context, id string) (item.Revision, error) {
	return s.store.Revision(ctx, id)
}
func (s *Service) Resolve(ctx context.Context, ids, kinds []string) (map[string]item.Item, error) {
	return s.store.Resolve(ctx, ids, kinds)
}
func (s *Service) Read(ctx context.Context, loc item.Location, maxBytes int64) (string, error) {
	if maxBytes < 1 || loc.Start < 0 || loc.End < loc.Start {
		return "", item.ErrInvalid
	}
	if e := s.store.Readable(ctx, loc.RevisionID); e != nil {
		return "", e
	}
	var key string
	byteStart, runeStart := int64(0), int64(0)
	if loc.SectionID != "" {
		v, e := s.store.Section(ctx, loc.RevisionID, loc.SectionID)
		if e != nil {
			return "", e
		}
		key = v.ObjectKey
		for _, point := range v.Seek {
			if point.Rune > loc.Start {
				break
			}
			byteStart = point.Byte
			runeStart = point.Rune
		}
		if loc.End == 0 {
			loc.End = v.Runes
		}
	} else {
		r, e := s.store.Revision(ctx, loc.RevisionID)
		if e != nil {
			return "", e
		}
		key = r.TextKey
	}
	if key == "" {
		return "", item.ErrUnavailable
	}
	reader, e := s.objects.Open(ctx, key, byteStart, -1)
	if e != nil {
		return "", e
	}
	defer reader.Close()
	br := bufio.NewReader(reader)
	var out strings.Builder
	pos := runeStart
	for {
		r, _, e := br.ReadRune()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return "", e
		}
		if pos >= loc.Start {
			if loc.End > 0 && pos >= loc.End {
				break
			}
			if int64(out.Len()+utf8.RuneLen(r)) > maxBytes {
				return "", fmt.Errorf("%w: requested passage exceeds budget", item.ErrInvalid)
			}
			out.WriteRune(r)
		}
		pos++
	}
	if loc.Start > pos || (loc.End > 0 && pos < loc.End) {
		return "", item.ErrInvalid
	}
	return out.String(), nil
}
func (s *Service) File(ctx context.Context, revision string, start, length int64) (io.ReadCloser, item.Revision, error) {
	r, e := s.store.Revision(ctx, revision)
	if e != nil {
		return nil, r, e
	}
	if e = s.store.Readable(ctx, revision); e != nil {
		return nil, r, e
	}
	if start < 0 || start >= r.Bytes && r.Bytes > 0 {
		return nil, r, item.ErrInvalid
	}
	reader, e := s.objects.Open(ctx, r.FileKey, start, length)
	return reader, r, e
}
func (s *Service) RecordEvents(ctx context.Context, events []item.Behavior) (int, error) {
	return s.store.RecordBehavior(ctx, events)
}
func (s *Service) RunOnce(ctx context.Context) (bool, error) {
	j, e := s.store.Claim(ctx, time.Duration(s.config.LeaseSeconds)*time.Second)
	if errors.Is(e, item.ErrNotFound) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		ticker := time.NewTicker(max(time.Second, time.Duration(s.config.LeaseSeconds)*time.Second/3))
		defer ticker.Stop()
		for {
			select {
			case <-run.Done():
				return
			case <-ticker.C:
				if e := s.store.Renew(run, j, time.Duration(s.config.LeaseSeconds)*time.Second); e != nil {
					cancel()
					return
				}
			}
		}
	}()
	var stage *telemetry.Stage
	if s.observed != nil {
		run, stage, _ = s.observed.Begin(run, "content", "content.process", slog.String("job_id", j.ID), slog.String("revision_id", j.RevisionID))
	}
	e = s.process(run, j)
	if stage != nil {
		outcome := "succeeded"
		if e != nil {
			outcome = "failed"
		}
		stage.End(run, outcome, "", e)
	}
	cancel()
	<-renewed
	if e != nil {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		permanent := errors.Is(e, processing.ErrUnsupported) || errors.Is(e, item.ErrInvalid) || j.Attempt >= s.config.MaxAttempts
		_ = s.store.Fail(cleanup, j, e, permanent)
		if permanent && j.Kind == "process" {
			_ = s.store.RevisionError(cleanup, j.RevisionID, e)
		}
	}
	return true, e
}
func (s *Service) process(ctx context.Context, j item.Job) error {
	if j.Kind == "prune" {
		if s.index == nil {
			return item.ErrUnavailable
		}
		if e := s.index.Prune(ctx, s.config.Retrieval, j.RevisionID); e != nil {
			return e
		}
		if s.lexical != nil {
			if e := s.lexical.PruneRevision(ctx, j.RevisionID); e != nil {
				return e
			}
		}
		if s.graph != nil {
			if e := s.graph.PruneRevision(ctx, j.RevisionID); e != nil {
				return e
			}
		}
		return s.store.CompleteMaintenance(ctx, j)
	}
	if e := s.store.Check(ctx, j); e != nil {
		return e
	}
	rev, e := s.store.Revision(ctx, j.RevisionID)
	if e != nil {
		return e
	}
	if j.Kind == "delete" {
		if s.index == nil {
			return item.ErrUnavailable
		}
		if e = s.index.Delete(ctx, s.config.Retrieval, rev.RootID); e != nil {
			return e
		}
		if s.lexical != nil {
			if e = s.lexical.DeleteRoot(ctx, rev.RootID); e != nil {
				return e
			}
		}
		if s.graph != nil {
			if e = s.graph.DeleteRoot(ctx, rev.RootID); e != nil {
				return e
			}
		}
		return s.store.CompleteMaintenance(ctx, j)
	}
	if rev.TextKey == "" {
		parser := processing.Parser{Objects: s.objects, MaxUncompressed: s.config.MaxUncompressedBytes, Attempt: j.Token}
		parsed, e := parser.Parse(ctx, rev)
		if e != nil {
			return e
		}
		if e = s.store.SaveNavigation(ctx, rev.ID, parsed.Navigation); e != nil {
			return e
		}
		if e = s.store.SaveParsed(ctx, j, parsed.Sections, parsed.TextKey, parsed.Digest); e != nil {
			return e
		}
	}
	retrieval := s.config.Retrieval
	if s.index == nil || s.analyzer == nil || s.encoder == nil {
		return item.ErrUnavailable
	}
	if e = s.store.Stage(ctx, j, "understanding"); e != nil {
		return e
	}
	digest, e := s.store.Digest(ctx, rev.ID)
	if e != nil {
		return e
	}
	root := item.Item{ID: rev.RootID, RootID: rev.RootID, Kind: rev.Kind, RootKind: rev.Kind, SourceID: rev.SourceID, RevisionID: rev.ID, Title: rev.Title, SourceURL: rev.SourceURL, DuplicateID: digest, Features: map[string]float64{}}
	if !rev.OccurredAt.IsZero() {
		root.Features["source_event_unix"] = float64(rev.OccurredAt.Unix())
	}
	summaries := []string{}
	languages := map[string]int{}
	covered := 0
	after := 0
	for {
		sections, e := s.store.Sections(ctx, rev.ID, after, 100)
		if e != nil {
			return e
		}
		if len(sections) == 0 {
			break
		}
		for _, section := range sections {
			if root.Location.SectionID == "" {
				root.Location = item.Location{RevisionID: rev.ID, SectionID: section.ID, Start: 0, End: section.Runes}
			}
			textReader, e := s.objects.Open(ctx, section.ObjectKey, 0, -1)
			if e != nil {
				return e
			}
			reader := bufio.NewReader(textReader)
			offset := int64(0)
			for {
				window, next, e := readWindow(reader, s.config.WindowRunes, offset)
				if e != nil {
					textReader.Close()
					return e
				}
				if window == "" {
					break
				}
				if strings.TrimSpace(window) == "" {
					offset = next
					continue
				}
				analysis, e := s.understand(ctx, rev.ID, section.ID, offset, section.Title, window)
				if e != nil {
					textReader.Close()
					return e
				}
				summaries = append(summaries, analysis.Summary)
				if analysis.Language != "" {
					languages[analysis.Language] += utf8.RuneCountInString(window)
				}
				root.Topics = append(root.Topics, analysis.Topics...)
				semantic := []item.Item{}
				relations := []item.Relation{}
				samples := []item.Sample{}
				for _, o := range analysis.Objects {
					byteStart := strings.Index(window, o.Quote)
					if byteStart < 0 || o.Quote == "" {
						textReader.Close()
						return fmt.Errorf("%w: ungrounded object", item.ErrInvalid)
					}
					start := offset + int64(utf8.RuneCountInString(window[:byteStart]))
					location := item.Location{RevisionID: rev.ID, SectionID: section.ID, Start: start, End: start + int64(utf8.RuneCountInString(o.Quote))}
					id := uuid.NewSHA1(uuid.NameSpaceURL, []byte(rev.ID+section.ID+fmt.Sprint(start)+o.Kind+o.Title)).String()
					v := item.Item{ID: id, RootID: root.ID, Kind: o.Kind, RootKind: root.Kind, SourceID: rev.SourceID, RevisionID: rev.ID, Title: o.Title, Summary: o.Summary, SourceURL: rev.SourceURL, DuplicateID: digest, Topics: analysis.Topics, Features: map[string]float64{}, Location: location}
					if analysis.Quality != nil {
						v.Features["quality"] = *analysis.Quality
					}
					v.Language = analysis.Language
					semantic = append(semantic, v)
					relations = append(relations, item.Relation{From: id, To: root.ID, Type: "part_of", Location: location})
				}
				names := map[string]string{}
				for _, v := range semantic {
					names[strings.ToLower(strings.TrimSpace(v.Title))] = v.ID
				}
				for _, relation := range analysis.Relations {
					from := names[strings.ToLower(strings.TrimSpace(relation.From))]
					to := names[strings.ToLower(strings.TrimSpace(relation.To))]
					if from == "" {
						from, _ = s.store.ByTitle(ctx, root.ID, rev.ID, relation.From)
					}
					if to == "" {
						to, _ = s.store.ByTitle(ctx, root.ID, rev.ID, relation.To)
					}
					if from == "" || to == "" || relation.Type == "" {
						continue
					}
					at := strings.Index(window, relation.Quote)
					if at < 0 || relation.Quote == "" {
						return item.ErrInvalid
					}
					start := offset + int64(utf8.RuneCountInString(window[:at]))
					relations = append(relations, item.Relation{From: from, To: to, Type: relation.Type, Location: item.Location{RevisionID: rev.ID, SectionID: section.ID, Start: start, End: start + int64(utf8.RuneCountInString(relation.Quote))}})
				}
				for _, q := range analysis.Queries {
					if q.Quote != "" && strings.Contains(window, q.Quote) {
						samples = append(samples, item.Sample{Query: q.Text, Positive: q.Quote, RootID: root.ID, DuplicateID: digest, SourceRevision: rev.ID, LabelSource: "synthetic", Label: q.Relevance, Group: uuid.NewSHA1(uuid.NameSpaceURL, []byte(q.Text)).String()})
					}
				}
				if e = s.store.SaveUnderstanding(ctx, j, semantic, relations, samples, covered); e != nil {
					textReader.Close()
					return e
				}
				if e = s.encodeWindow(ctx, retrieval, root, section.ID, offset, window); e != nil {
					textReader.Close()
					return e
				}
				for _, v := range semantic {
					if e = s.encodeWindow(ctx, retrieval, v, v.Location.SectionID, v.Location.Start, windowQuote(window, offset, v.Location)); e != nil {
						textReader.Close()
						return e
					}
				}
				offset = next
			}
			textReader.Close()
			covered++
			after = section.Order
			if e = s.store.SaveUnderstanding(ctx, j, nil, nil, nil, covered); e != nil {
				return e
			}
		}
	}
	summary, e := s.analyzer.Summarize(ctx, summaries)
	if e != nil {
		return e
	}
	root.Summary = summary
	root.Language = "und"
	best := 0
	for language, count := range languages {
		if count > best {
			best = count
			root.Language = language
		}
	}
	root.Topics = unique(root.Topics)
	if e = s.store.SaveUnderstanding(ctx, j, []item.Item{root}, nil, nil, covered); e != nil {
		return e
	}
	if e = s.store.Stage(ctx, j, "indexed"); e != nil {
		return e
	}
	if e = s.projectRevision(ctx, rev.ID); e != nil {
		return e
	}
	return s.store.Publish(ctx, j)
}
func windowQuote(text string, offset int64, loc item.Location) string {
	r := []rune(text)
	return string(r[loc.Start-offset : loc.End-offset])
}
func unique(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}
	return out
}
func readWindow(r *bufio.Reader, n int, start int64) (string, int64, error) {
	var b strings.Builder
	for i := 0; i < n; i++ {
		v, _, e := r.ReadRune()
		if errors.Is(e, io.EOF) {
			return b.String(), start + int64(i), nil
		}
		if e != nil {
			return "", start, e
		}
		b.WriteRune(v)
	}
	return b.String(), start + int64(n), nil
}
func (s *Service) encodeWindow(ctx context.Context, retrieval item.Retrieval, v item.Item, section string, offset int64, text string) error {
	encoded, e := s.encoding(ctx, retrieval, v, section, offset, text)
	if e != nil {
		return e
	}
	for _, part := range encoded[0].Segments {
		loc := item.Location{RevisionID: v.RevisionID, SectionID: section, Start: offset + part.Start, End: offset + part.End}
		id := uuid.NewSHA1(uuid.NameSpaceURL, []byte(v.ID+v.RevisionID+section+fmt.Sprint(loc.Start))).String()
		record := idx.Record{ID: id, ItemID: v.ID, RootID: v.RootID, RootKind: v.RootKind, RevisionID: v.RevisionID, Location: loc, Encoding: part}
		if retrieval.ItemTower != "" {
			projector, ok := s.encoder.(inference.Projector)
			if !ok {
				return item.ErrUnavailable
			}
			vectors, e := projector.Project(ctx, retrieval.ItemTower, retrieval.UserSpace, retrieval.Model, retrieval.Space, [][]float32{part.Dense})
			if e != nil {
				return e
			}
			record.Match = vectors[0]
		}
		if e = s.index.Upsert(ctx, retrieval, []idx.Record{record}); e != nil {
			return e
		}
	}
	return nil
}

// Retain source bytes independently from downstream model availability.
func IsUnsupported(err error) bool { return errors.Is(err, processing.ErrUnsupported) }

var _ item.Reader = (*Service)(nil)

func (s *Service) Retry(ctx context.Context, id string) error {
	job, e := s.store.Job(ctx, id)
	if e != nil {
		return e
	}
	revision, e := s.store.Revision(ctx, job.RevisionID)
	if e != nil {
		return e
	}
	var source catalog.Source
	if e = s.store.DB.WithContext(ctx).First(&source, "id=?", revision.SourceID).Error; e != nil {
		return e
	}
	if job.Kind != "prune" && source.Latest != revision.Number {
		return item.ErrStale
	}
	return s.store.DB.WithContext(ctx).Model(&catalog.Job{}).Where("id=? AND state IN ?", id, []string{"failed", "retry"}).Updates(map[string]any{"state": "pending", "next_run": time.Now().UTC(), "error": "", "attempt": 0}).Error
}

func newCatalog(db *gorm.DB, partitions int) *catalog.Store {
	store := catalog.New(db)
	store.Partitions = partitions
	return store
}

func (s *Service) Navigation(ctx context.Context, revision string, after, limit int) ([]item.Navigation, error) {
	return s.store.Navigation(ctx, revision, after, limit)
}
