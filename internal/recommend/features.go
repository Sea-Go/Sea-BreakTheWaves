// Package recommend accepts fixed RTW content generations for recommendation.
// It never treats warehouse fixtures or unobserved candidates as behavior.
package recommend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
)

var (
	ErrInvalid     = errors.New("invalid item publication or feature")
	ErrStale       = errors.New("RTW content publication changed")
	ErrConflict    = errors.New("immutable item release conflicts")
	ErrUnavailable = errors.New("item candidates unavailable")
)

const (
	MaxItems       = 4096
	MaxCandidates  = 100
	FeatureVersion = "rtw-item-metadata.v1"
)

// Publication is projected from RTW's current manual release. The three index
// refs bind this baseline pool to the same content generation; they are not a
// recommendation item-embedding index or an approved user/item pair.
type Publication struct {
	ModuleID            string                `json:"module_id"`
	ReleaseID           string                `json:"release_id"`
	Generation          int64                 `json:"generation"`
	PublicationRevision string                `json:"publication_revision"`
	Indexes             map[string]corpus.Ref `json:"indexes"`
	ValidRevisionIDs    []string              `json:"valid_revision_ids"`
}

// Revision comes only from RTW's revision Worker API after the above
// publication was read; Source implementations must not supply a newer head.
type Revision struct {
	RevisionID  string
	ModuleID    string
	ItemID      string
	Kind        string
	Title       string
	MediaType   string
	ObjectKey   string
	ContentHash string
	CreatedAt   time.Time
	Withdrawn   bool
}

type ContentSource interface {
	Current(context.Context, string) (Publication, error)
	Revision(context.Context, string) (Revision, error)
}

type ItemFeature struct {
	ItemID      string    `json:"item_id"`
	RevisionID  string    `json:"revision_id"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	MediaType   string    `json:"media_type"`
	ContentHash string    `json:"content_hash"`
	CreatedAt   time.Time `json:"created_at"`
}

type PoolRelease struct {
	ID             string        `json:"pool_release_id"`
	Publication    Publication   `json:"publication"`
	FeatureVersion string        `json:"feature_version"`
	FeatureHash    string        `json:"feature_hash"`
	Items          []ItemFeature `json:"items"`
}

func featureHash() string {
	spec := struct {
		Version string   `json:"version"`
		Fields  []string `json:"fields"`
	}{FeatureVersion, []string{"item_id", "revision_id", "kind", "title", "media_type", "content_hash", "created_at"}}
	raw, _ := json.Marshal(spec)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func validID(value string) bool {
	if value == "" || len(value) > 200 {
		return false
	}
	for _, c := range value {
		if c != '-' && c != '_' && c != '.' && (c < '0' || c > '9') &&
			(c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

func validPublication(p Publication) bool {
	if !validID(p.ModuleID) || !validID(p.ReleaseID) || p.Generation < 1 ||
		len(p.ValidRevisionIDs) > MaxItems || len(p.Indexes) != 3 {
		return false
	}
	pointer, err := strconv.ParseInt(p.PublicationRevision, 10, 64)
	if err != nil || pointer < 1 || strconv.FormatInt(pointer, 10) != p.PublicationRevision {
		return false
	}
	for _, lane := range []string{"dense", "sparse", "multivector"} {
		ref, present := p.Indexes[lane]
		if !present || !artifacts.ValidHash(ref.SHA256) || ref.Key != "sha256/"+ref.SHA256 {
			return false
		}
	}
	seen := make(map[string]bool, len(p.ValidRevisionIDs))
	for _, id := range p.ValidRevisionIDs {
		if !validID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func clonePublication(p Publication) Publication {
	copy := p
	if p.ValidRevisionIDs != nil {
		copy.ValidRevisionIDs = append([]string{}, p.ValidRevisionIDs...)
	}
	copy.Indexes = make(map[string]corpus.Ref, len(p.Indexes))
	for lane, ref := range p.Indexes {
		copy.Indexes[lane] = ref
	}
	return copy
}

func validItem(p Publication, revision Revision) bool {
	if !validID(revision.RevisionID) || !validID(revision.ItemID) || revision.ModuleID != p.ModuleID ||
		revision.Withdrawn || !artifacts.ValidHash(revision.ContentHash) ||
		revision.ObjectKey != "sha256/"+revision.ContentHash ||
		strings.TrimSpace(revision.Title) == "" || len(revision.Title) > 1024 ||
		strings.TrimSpace(revision.MediaType) == "" || len(revision.MediaType) > 200 ||
		revision.CreatedAt.IsZero() || (revision.Kind != "source" && revision.Kind != "wiki") {
		return false
	}
	return true
}

// BuildPool reads RTW's current pointer twice. A changed publication or an
// unavailable/withdrawn member rejects the whole candidate release.
func BuildPool(ctx context.Context, source ContentSource, moduleID string) (PoolRelease, error) {
	if ctx == nil || source == nil || !validID(moduleID) {
		return PoolRelease{}, ErrInvalid
	}
	publication, err := source.Current(ctx, moduleID)
	if err != nil {
		return PoolRelease{}, err
	}
	if publication.ModuleID != moduleID || !validPublication(publication) {
		return PoolRelease{}, ErrInvalid
	}
	publication = clonePublication(publication)
	items := make([]ItemFeature, 0, len(publication.ValidRevisionIDs))
	seenItems := make(map[string]bool, len(publication.ValidRevisionIDs))
	for _, revisionID := range publication.ValidRevisionIDs {
		if err := ctx.Err(); err != nil {
			return PoolRelease{}, err
		}
		revision, err := source.Revision(ctx, revisionID)
		if err != nil {
			return PoolRelease{}, fmt.Errorf("read RTW published revision %s: %w", revisionID, err)
		}
		if revision.RevisionID != revisionID || !validItem(publication, revision) || seenItems[revision.ItemID] {
			return PoolRelease{}, ErrInvalid
		}
		seenItems[revision.ItemID] = true
		items = append(items, ItemFeature{ItemID: revision.ItemID, RevisionID: revisionID,
			Kind: revision.Kind, Title: revision.Title, MediaType: revision.MediaType,
			ContentHash: revision.ContentHash, CreatedAt: revision.CreatedAt.UTC()})
	}
	again, err := source.Current(ctx, moduleID)
	if err != nil {
		return PoolRelease{}, err
	}
	if !reflect.DeepEqual(publication, again) {
		return PoolRelease{}, ErrStale
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ItemID < items[j].ItemID })
	release := PoolRelease{Publication: publication, FeatureVersion: FeatureVersion,
		FeatureHash: featureHash(), Items: items}
	id, err := releaseHash(release)
	if err != nil {
		return PoolRelease{}, err
	}
	release.ID = id
	return release, nil
}

func releaseHash(release PoolRelease) (string, error) {
	release.ID = ""
	raw, err := json.Marshal(release)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func validRelease(release PoolRelease) bool {
	if !validPublication(release.Publication) || release.FeatureVersion != FeatureVersion ||
		release.FeatureHash != featureHash() || len(release.Items) != len(release.Publication.ValidRevisionIDs) {
		return false
	}
	seen := make(map[string]bool, len(release.Items))
	seenRevisions := make(map[string]bool, len(release.Items))
	allowed := make(map[string]bool, len(release.Publication.ValidRevisionIDs))
	for _, id := range release.Publication.ValidRevisionIDs {
		allowed[id] = true
	}
	for i, item := range release.Items {
		if !validID(item.ItemID) || !allowed[item.RevisionID] || seen[item.ItemID] || seenRevisions[item.RevisionID] ||
			!artifacts.ValidHash(item.ContentHash) || item.CreatedAt.IsZero() ||
			(item.Kind != "source" && item.Kind != "wiki") || strings.TrimSpace(item.Title) == "" ||
			strings.TrimSpace(item.MediaType) == "" || len(item.Title) > 1024 || len(item.MediaType) > 200 ||
			(i > 0 && release.Items[i-1].ItemID >= item.ItemID) {
			return false
		}
		seen[item.ItemID] = true
		seenRevisions[item.RevisionID] = true
	}
	hash, err := releaseHash(release)
	return err == nil && hash == release.ID
}
