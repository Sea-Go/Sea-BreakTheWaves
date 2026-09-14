package recommend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
)

const (
	MaxMatureRows        = 100_000
	MaxPositivePerPerson = 100
	MaxNeighbors         = 100
	ItemCFVersion        = "normalized-cooccurrence.v1"
)

// MatureObservation is an H10.a DWS result, not an event or a recommendation
// stage candidate. A future source adapter must prove its generation/manifest
// and JOIN qualification before this value is used outside isolated tests.
type MatureObservation struct {
	SampleID      string    `json:"sample_id"`
	AuthorityID   string    `json:"authority_id"`
	TenantID      string    `json:"tenant_id"`
	SubjectID     string    `json:"subject_id"`
	RequestID     string    `json:"request_id"`
	ImpressionID  string    `json:"impression_id"`
	ItemID        string    `json:"item_id"`
	RevisionID    string    `json:"content_revision"`
	Qualification string    `json:"qualification"`
	LabelState    string    `json:"label_state"` // POSITIVE or OBSERVED_NEGATIVE
	WindowMature  bool      `json:"window_mature"`
	WindowEnd     time.Time `json:"window_end"`
	AvailableAt   time.Time `json:"available_at"` // label availability, not a later request feature
}

type MatureBatch struct {
	Generation        string              `json:"generation"`
	ManifestRef       string              `json:"manifest_ref"`
	ManifestHash      string              `json:"manifest_hash"`
	DefinitionVersion string              `json:"definition_version"`
	Watermark         time.Time           `json:"watermark"`
	Rows              []MatureObservation `json:"rows"`
}

type MatureSource interface {
	ReadMature(context.Context, PoolRelease) (MatureBatch, error)
}

type Neighbor struct {
	ItemID       string  `json:"item_id"`
	RevisionID   string  `json:"revision_id"`
	Score        float64 `json:"score"`
	SupportUsers int     `json:"support_users"`
}

type ItemNeighbors struct {
	ItemID     string     `json:"item_id"`
	RevisionID string     `json:"revision_id"`
	Neighbors  []Neighbor `json:"neighbors"`
}

// ItemCFArtifact is a candidate computation. It is never an active pool or
// model/pair release; WS07's real DWS source and durable approval are pending.
type ItemCFArtifact struct {
	ID                string          `json:"itemcf_artifact_id"`
	PoolReleaseID     string          `json:"pool_release_id"`
	Version           string          `json:"version"`
	TopK              int             `json:"top_k"`
	Generation        string          `json:"dws_generation"`
	ManifestRef       string          `json:"manifest_ref"`
	ManifestHash      string          `json:"manifest_hash"`
	DefinitionVersion string          `json:"definition_version"`
	Watermark         time.Time       `json:"watermark"`
	BehaviorState     string          `json:"behavior_state"` // no_mature_rows, mature_no_positive, positive
	MatureRows        int             `json:"mature_rows"`
	PositiveRows      int             `json:"positive_rows"`
	ObservedNegative  int             `json:"observed_negative_rows"`
	ExcludedOldItems  int             `json:"excluded_old_items"`
	Items             []ItemNeighbors `json:"items"`
}

type subjectKey struct{ authority, tenant, subject string }
type impressionKey struct {
	subject    subjectKey
	impression string
}
type pairKey struct{ from, to string }
type pairStats struct {
	weight float64
	users  int
}

// ComputeItemCF reads a fixed mature DWS batch and performs the one approved
// numerical baseline: per-user capped 1/(n-1) cooccurrence, divided by the
// square root of distinct-positive-user frequencies. It cannot activate the
// result because a typed batch alone is not a real CH/S3 provenance receipt.
func ComputeItemCF(ctx context.Context, release PoolRelease, source MatureSource, topK int) (ItemCFArtifact, error) {
	if ctx == nil || !validRelease(release) || source == nil || topK < 1 || topK > MaxNeighbors {
		return ItemCFArtifact{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return ItemCFArtifact{}, err
	}
	batch, err := source.ReadMature(ctx, release)
	if err != nil {
		return ItemCFArtifact{}, err
	}
	if !validID(batch.Generation) || !validID(batch.DefinitionVersion) || batch.ManifestRef == "" ||
		len(batch.ManifestRef) > 1024 || !artifacts.ValidHash(batch.ManifestHash) ||
		batch.Watermark.IsZero() || len(batch.Rows) > MaxMatureRows {
		return ItemCFArtifact{}, ErrInvalid
	}
	current := make(map[string]string, len(release.Items))
	for _, item := range release.Items {
		current[item.ItemID] = item.RevisionID
	}
	positives := make(map[subjectKey]map[string]bool)
	seenSamples := make(map[string]bool, len(batch.Rows))
	seenImpressions := make(map[impressionKey]bool, len(batch.Rows))
	artifact := ItemCFArtifact{PoolReleaseID: release.ID, Version: ItemCFVersion, TopK: topK,
		Generation: batch.Generation, ManifestRef: batch.ManifestRef, ManifestHash: batch.ManifestHash,
		DefinitionVersion: batch.DefinitionVersion, Watermark: batch.Watermark.UTC(),
		MatureRows: len(batch.Rows), Items: []ItemNeighbors{}}
	for _, row := range batch.Rows {
		if err := ctx.Err(); err != nil {
			return ItemCFArtifact{}, err
		}
		if !validID(row.SampleID) || !validID(row.AuthorityID) || !validID(row.TenantID) ||
			!validID(row.SubjectID) || !validID(row.RequestID) || !validID(row.ImpressionID) ||
			!validID(row.ItemID) || !validID(row.RevisionID) || seenSamples[row.SampleID] ||
			row.Qualification != "eligible" || !row.WindowMature || row.WindowEnd.IsZero() ||
			row.AvailableAt.IsZero() || row.AvailableAt.Before(row.WindowEnd) ||
			row.WindowEnd.After(batch.Watermark) ||
			row.AvailableAt.After(batch.Watermark) ||
			(row.LabelState != "POSITIVE" && row.LabelState != "OBSERVED_NEGATIVE") {
			return ItemCFArtifact{}, ErrInvalid
		}
		person := subjectKey{row.AuthorityID, row.TenantID, row.SubjectID}
		impression := impressionKey{person, row.ImpressionID}
		if seenImpressions[impression] {
			return ItemCFArtifact{}, ErrInvalid
		}
		seenSamples[row.SampleID] = true
		seenImpressions[impression] = true
		if current[row.ItemID] != row.RevisionID {
			artifact.ExcludedOldItems++
			continue
		}
		if row.LabelState == "OBSERVED_NEGATIVE" {
			artifact.ObservedNegative++
			continue
		}
		artifact.PositiveRows++
		if positives[person] == nil {
			positives[person] = make(map[string]bool)
		}
		positives[person][row.ItemID] = true
		if len(positives[person]) > MaxPositivePerPerson {
			return ItemCFArtifact{}, ErrInvalid
		}
	}
	artifact.BehaviorState = "positive"
	if len(batch.Rows) == 0 {
		artifact.BehaviorState = "no_mature_rows"
	} else if artifact.PositiveRows == 0 {
		artifact.BehaviorState = "mature_no_positive"
	}
	freq := make(map[string]int)
	pairs := make(map[pairKey]pairStats)
	for _, items := range positives {
		if err := ctx.Err(); err != nil {
			return ItemCFArtifact{}, err
		}
		ids := make([]string, 0, len(items))
		for id := range items {
			ids = append(ids, id)
			freq[id]++
		}
		sort.Strings(ids)
		if len(ids) < 2 {
			continue
		}
		weight := 1 / float64(len(ids)-1)
		for _, from := range ids {
			for _, to := range ids {
				if from == to {
					continue
				}
				key := pairKey{from, to}
				stats := pairs[key]
				stats.weight += weight
				stats.users++
				pairs[key] = stats
			}
		}
	}
	byItem := make(map[string][]Neighbor)
	for pair, stats := range pairs {
		score := stats.weight / math.Sqrt(float64(freq[pair.from])*float64(freq[pair.to]))
		byItem[pair.from] = append(byItem[pair.from], Neighbor{ItemID: pair.to,
			RevisionID: current[pair.to], Score: score, SupportUsers: stats.users})
	}
	for from, neighbors := range byItem {
		sort.Slice(neighbors, func(i, j int) bool {
			if neighbors[i].Score == neighbors[j].Score {
				return neighbors[i].ItemID < neighbors[j].ItemID
			}
			return neighbors[i].Score > neighbors[j].Score
		})
		if len(neighbors) > topK {
			neighbors = neighbors[:topK]
		}
		artifact.Items = append(artifact.Items, ItemNeighbors{ItemID: from,
			RevisionID: current[from], Neighbors: neighbors})
	}
	sort.Slice(artifact.Items, func(i, j int) bool { return artifact.Items[i].ItemID < artifact.Items[j].ItemID })
	raw, err := json.Marshal(artifact)
	if err != nil {
		return ItemCFArtifact{}, err
	}
	sum := sha256.Sum256(raw)
	artifact.ID = hex.EncodeToString(sum[:])
	return artifact, nil
}
