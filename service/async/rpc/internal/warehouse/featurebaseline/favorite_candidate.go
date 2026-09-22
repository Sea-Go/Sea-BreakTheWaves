package featurebaseline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/usermodel"
)

const favoriteProducer = "rtw.community.favorite"
const favoritePartition = "dc:rtw.community.favorite"

// FavoriteCandidateSpec is deliberately unapproved and default-off. Approval
// belongs to the user-model product rule owner, not the warehouse producer.
func FavoriteCandidateSpec() usermodel.FeatureSpec {
	return usermodel.FeatureSpec{Version: "candidate-favorite-active-count-v1", Features: []usermodel.FeatureDefinition{{
		Name: "favorite_active_count", Source: "fact", Kind: usermodel.ProductAction,
		Predicate: "favorite", Mode: "count", Default: "0",
	}}}
}

// FavoriteDWDRef is a content-addressed archive produced by the warehouse's
// own DC consumer. It is not a recommendation exposure or mature label source.
type FavoriteDWDRef struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type FavoriteCandidateManifest struct {
	Status          string               `json:"status"` // candidate_default_off
	ArtifactSHA256  string               `json:"-"`
	Subject         usermodel.SubjectRef `json:"subject_ref"`
	Generation      string               `json:"generation"`
	Revision        int64                `json:"revision"`
	FeatureSpecHash string               `json:"feature_spec_hash"`
	DWD             FavoriteDWDRef       `json:"favorite_dwd"`
	DWDPrefixSHA256 string               `json:"dwd_prefix_sha256"`
	ThroughOffset   int64                `json:"through_offset"`
	ActiveCount     int                  `json:"active_count"`
	SourceWatermark usermodel.Watermark  `json:"source_watermark"`
	FeatureBaseline Manifest             `json:"feature_baseline"`
}

type favoriteDWDRow struct {
	Producer           string  `json:"producer"`
	SourceOffset       int64   `json:"source_offset"`
	EventID            string  `json:"event_id"`
	EventType          string  `json:"event_type"`
	AuthorityID        string  `json:"authority_id"`
	TenantID           string  `json:"tenant_id"`
	SubjectID          string  `json:"subject_id"`
	FavoriteID         string  `json:"favorite_id"`
	FolderID           string  `json:"folder_id"`
	TargetType         string  `json:"target_type"`
	TargetID           string  `json:"target_id"`
	TargetRevision     *string `json:"target_revision"`
	Operation          string  `json:"operation"`
	PredecessorEventID string  `json:"predecessor_event_id"`
	FavoriteStateDelta int     `json:"favorite_state_delta"`
	ActiveAfter        int     `json:"active_after"`
	EventTime          string  `json:"event_time"`
	AvailableAt        string  `json:"available_at"`
	DCReceivedAt       string  `json:"dc_received_at"`
	SourceEventHash    string  `json:"source_event_hash"`
}

type favoriteDWDState struct {
	PrefixHash string
	Count      int
	Active     map[string]favoriteDWDRow
	Rows       []favoriteDWDRow
}

// BuildFavoriteCandidate requires two independent proofs: the frozen DWD
// source prefix and the usermodel PG accepted ledger/contiguous watermark.
// The existing Runner then freezes the FeatureBaseline. This function never
// accepts a baseline, authorizes a pair, or activates a ServingBundle.
func (r Runner) BuildFavoriteCandidate(ctx context.Context, subject usermodel.SubjectRef,
	revision int64, generation string, ref FavoriteDWDRef, throughOffset int64) (FavoriteCandidateManifest, error) {
	var out FavoriteCandidateManifest
	if err := r.check(); err != nil {
		return out, err
	}
	spec, specHash, err := usermodel.PrepareFeatureSpec(FavoriteCandidateSpec())
	if err != nil {
		return out, err
	}
	if !digest.MatchString(ref.SHA256) || !strings.HasPrefix(ref.URL, strings.TrimRight(r.S3Prefix, "/")+"/warehouse-favorite/dwd/") ||
		!strings.HasSuffix(ref.URL, "/"+ref.SHA256+".jsonl") {
		return out, errors.New("favorite DWD content-addressed source required")
	}
	parsed, err := url.Parse(ref.URL)
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return out, errors.New("favorite DWD URL scope")
	}
	body, err := r.getFixed(ctx, ref.URL, ref.SHA256)
	if err != nil {
		return out, err
	}
	state, err := verifyFavoriteDWD(body, subject, throughOffset)
	if err != nil {
		return out, err
	}
	projection, err := r.Source.Current(ctx, subject)
	if err != nil {
		return out, err
	}
	if err := checkFavoriteProjection(ctx, r.Source, projection, state, throughOffset); err != nil {
		return out, err
	}
	m, err := r.Build(ctx, subject, revision, generation, spec)
	if err != nil {
		return out, err
	}
	watermark := usermodel.Watermark{Producer: favoriteProducer, SourcePartition: favoritePartition,
		ContiguousSequence: throughOffset, MaxSeenSequence: throughOffset, Complete: true}
	if m.SpecHash != specHash || m.InputStateVersion != projection.StateVersion ||
		m.ContributionCount != state.Count || len(m.Watermarks) != 1 || !reflect.DeepEqual(m.Watermarks[0], watermark) {
		return out, errors.New("favorite DWD and built feature baseline changed across snapshot")
	}
	out = FavoriteCandidateManifest{Status: "candidate_default_off", Subject: subject, Generation: generation, Revision: revision,
		FeatureSpecHash: specHash, DWD: ref, DWDPrefixSHA256: state.PrefixHash, ThroughOffset: throughOffset,
		ActiveCount: state.Count, SourceWatermark: watermark, FeatureBaseline: m}
	artifact, err := json.Marshal(out)
	if err != nil {
		return FavoriteCandidateManifest{}, err
	}
	out.ArtifactSHA256 = hash(artifact)
	if err := r.putFixed(ctx, r.objectURL(generation, "favorite-candidate/"+out.ArtifactSHA256+".json"), artifact); err != nil {
		return FavoriteCandidateManifest{}, err
	}
	return out, nil
}

func verifyFavoriteDWD(body []byte, subject usermodel.SubjectRef, through int64) (favoriteDWDState, error) {
	var result favoriteDWDState
	if through < 1 || through > 100000 || len(body) == 0 || len(body) > 32<<20 {
		return result, errors.New("favorite DWD prefix unavailable")
	}
	active := map[string]favoriteDWDRow{}
	seenEvents := map[string]bool{}
	seenFavorites := map[string]bool{}
	var prefix bytes.Buffer
	for _, line := range bytes.Split(bytes.TrimSpace(body), []byte{'\n'}) {
		var row favoriteDWDRow
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&row); err != nil {
			return result, fmt.Errorf("favorite DWD row: %w", err)
		}
		if decoder.Decode(new(any)) != io.EOF {
			return result, errors.New("favorite DWD trailing JSON")
		}
		offset := int64(len(result.Rows) + 1)
		favoriteID, favoriteErr := strconv.ParseInt(row.FavoriteID, 10, 64)
		folderID, folderErr := strconv.ParseInt(row.FolderID, 10, 64)
		if favoriteErr != nil || folderErr != nil || favoriteID < 1 || folderID < 1 ||
			strconv.FormatInt(favoriteID, 10) != row.FavoriteID || strconv.FormatInt(folderID, 10) != row.FolderID {
			return result, errors.New("favorite DWD identifier must be exact decimal string")
		}
		if _, err := parseDWDTime(row.EventTime); err != nil {
			return result, err
		}
		if _, err := parseDWDTime(row.AvailableAt); err != nil {
			return result, err
		}
		if _, err := parseDWDTime(row.DCReceivedAt); err != nil {
			return result, err
		}
		if row.SourceOffset != offset || row.Producer != favoriteProducer ||
			row.AuthorityID != subject.AuthorityID || row.TenantID != subject.TenantID || row.SubjectID != subject.SubjectID ||
			row.FavoriteID == "" || row.FolderID == "" || row.TargetType == "" || row.TargetID == "" || !digest.MatchString(row.SourceEventHash) ||
			row.EventTime == "" || row.AvailableAt == "" || row.DCReceivedAt == "" {
			return result, errors.New("favorite DWD offset, subject or provenance mismatch")
		}
		if seenEvents[row.EventID] {
			return result, errors.New("duplicate favorite DWD event")
		}
		seenEvents[row.EventID] = true
		if offset <= through {
			prefix.Write(line)
			prefix.WriteByte('\n')
			switch row.Operation {
			case "assert":
				if row.EventType != "rtw.favorite.assert" || row.EventID != "favorite."+row.FavoriteID+".v1" ||
					row.PredecessorEventID != "" || row.FavoriteStateDelta != 1 || row.ActiveAfter != 1 {
					return result, errors.New("favorite assert DWD transition mismatch")
				}
				if seenFavorites[row.FavoriteID] {
					return result, errors.New("duplicate active favorite")
				}
				seenFavorites[row.FavoriteID] = true
				active[row.FavoriteID] = row
			case "retract":
				old, ok := active[row.FavoriteID]
				if !ok || row.EventType != "rtw.favorite.retract" || row.EventID != "favorite."+row.FavoriteID+".v2" ||
					row.PredecessorEventID != old.EventID || row.FavoriteStateDelta != -1 || row.ActiveAfter != 0 ||
					row.TargetType != old.TargetType || row.TargetID != old.TargetID || !reflect.DeepEqual(row.TargetRevision, old.TargetRevision) {
					return result, errors.New("orphan or changed-target favorite retract")
				}
				delete(active, row.FavoriteID)
			default:
				return result, errors.New("unknown favorite DWD operation")
			}
		}
		result.Rows = append(result.Rows, row)
	}
	if int64(len(result.Rows)) < through {
		return result, errors.New("favorite DWD source gap")
	}
	result.PrefixHash = hash(prefix.Bytes())
	result.Count = len(active)
	result.Active = active
	return result, nil
}

func checkFavoriteProjection(ctx context.Context, store *usermodel.Store, projection usermodel.Projection,
	state favoriteDWDState, through int64) error {
	if projection.StateVersion != through || len(projection.Watermarks) != 1 ||
		projection.Watermarks[0].Producer != favoriteProducer || projection.Watermarks[0].SourcePartition != favoritePartition ||
		projection.Watermarks[0].ContiguousSequence != through || projection.Watermarks[0].MaxSeenSequence != through || !projection.Watermarks[0].Complete {
		return errors.New("favorite accepted PG source watermark differs from DWD prefix")
	}
	history, err := store.History(ctx, projection.Subject, 1000)
	if err != nil || len(history) != int(through) {
		return errors.New("favorite DWD prefix and PG accepted history differ")
	}
	byOffset := make(map[int64]usermodel.Fact, len(history))
	for _, fact := range history {
		if fact.Status != "accepted" || fact.Producer != favoriteProducer || fact.SourcePartition != favoritePartition ||
			fact.SourceSequence < 1 || fact.SourceSequence > through {
			return errors.New("favorite PG history has an unbound source")
		}
		if _, exists := byOffset[fact.SourceSequence]; exists {
			return errors.New("favorite PG history duplicate source offset")
		}
		byOffset[fact.SourceSequence] = fact
	}
	for _, row := range state.Rows[:int(through)] {
		receipt, err := store.CurrentReceipt(ctx, projection.Subject, usermodel.EventKey{Producer: favoriteProducer, EventID: row.EventID})
		if err != nil || receipt.Status != "accepted" {
			return errors.New("favorite DWD row lacks accepted PG fact receipt")
		}
		fact, found := byOffset[row.SourceOffset]
		occurredAt, err1 := parseDWDTime(row.EventTime)
		receivedAt, err2 := parseDWDTime(row.DCReceivedAt)
		valueRef := row.TargetType + "/" + row.TargetID
		if row.TargetRevision != nil {
			valueRef += "/revision/" + *row.TargetRevision
		}
		if !found || err1 != nil || err2 != nil || fact.EventID != row.EventID || fact.EvidenceHash != row.SourceEventHash ||
			fact.ValueRef != valueRef || !fact.OccurredAt.Truncate(time.Microsecond).Equal(occurredAt.Truncate(time.Microsecond)) ||
			!fact.ObservedAt.Truncate(time.Microsecond).Equal(receivedAt.Truncate(time.Microsecond)) ||
			(row.Operation == "assert" && fact.Action != usermodel.Assert) || (row.Operation == "retract" && fact.Action != usermodel.Retract) {
			return errors.New("favorite DWD source hash, revision or time differs from PG accepted history")
		}
	}
	if len(projection.Active) != state.Count {
		return errors.New("favorite DWD count differs from PG active facts")
	}
	for _, fact := range projection.Active {
		row, ok := state.Active[strings.TrimSuffix(strings.TrimPrefix(fact.EventID, "favorite."), ".v1")]
		valueRef := row.TargetType + "/" + row.TargetID
		if row.TargetRevision != nil {
			valueRef += "/revision/" + *row.TargetRevision
		}
		if !ok || fact.Kind != usermodel.ProductAction || fact.Predicate != "favorite" || fact.Action != usermodel.Assert ||
			fact.EvidenceHash != row.SourceEventHash || fact.SourceSequence != row.SourceOffset || fact.ValueRef != valueRef {
			return errors.New("favorite DWD active contribution differs from PG fact")
		}
	}
	return nil
}

// The candidate source times are conserved separately: RTW available_at is
// business availability; DC receipt time is the PG observed_at boundary.
func parseDWDTime(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999"} {
		if value, err := time.Parse(layout, raw); err == nil {
			return value.UTC(), nil
		}
	}
	return time.Time{}, errors.New("favorite DWD source time invalid")
}
