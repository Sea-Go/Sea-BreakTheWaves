package usermodel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"time"
)

// FeatureSpec is approved by the user-model owner. The warehouse and this
// bounded near-line projection must use the same immutable version and hash.
type FeatureSpec struct {
	Version  string              `json:"version"`
	Features []FeatureDefinition `json:"features"`
}

type FeatureDefinition struct {
	Name          string       `json:"name"`
	Source        string       `json:"source"` // fact or ontology_rule
	Kind          SemanticKind `json:"kind,omitempty"`
	Predicate     string       `json:"predicate,omitempty"`
	Rule          string       `json:"rule,omitempty"`
	Mode          string       `json:"mode"` // count or latest; ontology_rule only count
	WindowSeconds int64        `json:"window_seconds,omitempty"`
	Default       string       `json:"default"`
	Vocabulary    []string     `json:"vocabulary,omitempty"`
	OOV           string       `json:"oov,omitempty"`
}

type FeatureValue struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Missing bool   `json:"missing"`
	OOV     bool   `json:"oov"`
}

// A baseline carries the reversible event-level contribution ledger. An
// aggregate without these immutable keys cannot subtract a later withdrawal.
// Every active accepted fact at or below a declared source watermark is listed,
// even if it contributes to no feature in the current spec.
type FeatureBaseline struct {
	Subject       SubjectRef     `json:"subject_ref"`
	Revision      int64          `json:"revision"`
	Generation    string         `json:"generation"`
	SpecVersion   string         `json:"feature_spec_version"`
	SpecHash      string         `json:"feature_spec_hash"`
	AsOf          time.Time      `json:"as_of"`
	AvailableAt   time.Time      `json:"available_at"`
	Watermarks    []Watermark    `json:"watermarks"`
	Contributions []Fact         `json:"contributions"`
	Values        []FeatureValue `json:"values"`
}

type FeatureSnapshot struct {
	ID              string         `json:"feature_snapshot_id"`
	Subject         SubjectRef     `json:"subject_ref"`
	SpecVersion     string         `json:"feature_spec_version"`
	SpecHash        string         `json:"feature_spec_hash"`
	StateVersion    int64          `json:"user_state_version"`
	OntologyVersion int64          `json:"ontology_definition_version,omitempty"`
	AsOf            time.Time      `json:"as_of"`
	AvailableAt     time.Time      `json:"available_at"`
	NextChangeAt    *time.Time     `json:"next_change_at,omitempty"`
	BaselineState   string         `json:"baseline_state"` // accepted or absent
	Generation      string         `json:"baseline_generation,omitempty"`
	Revision        int64          `json:"baseline_revision,omitempty"`
	Watermarks      []Watermark    `json:"source_watermarks"`
	Tail            []EventKey     `json:"tail_event_keys"`
	Values          []FeatureValue `json:"values"`
}

func canonicalFeatureSpec(input FeatureSpec) (FeatureSpec, string, error) {
	spec := input
	if !token.MatchString(spec.Version) || len(spec.Features) == 0 || len(spec.Features) > 128 {
		return FeatureSpec{}, "", fmt.Errorf("%w: feature spec version or size", ErrInvalid)
	}
	spec.Features = slices.Clone(input.Features)
	sort.Slice(spec.Features, func(i, j int) bool { return spec.Features[i].Name < spec.Features[j].Name })
	for i := range spec.Features {
		f := &spec.Features[i]
		if !token.MatchString(f.Name) || (i > 0 && spec.Features[i-1].Name == f.Name) || f.WindowSeconds < 0 || f.WindowSeconds > 10*365*24*3600 {
			return FeatureSpec{}, "", fmt.Errorf("%w: feature name or window", ErrInvalid)
		}
		switch f.Source {
		case "fact":
			if !token.MatchString(f.Predicate) || f.Rule != "" {
				return FeatureSpec{}, "", fmt.Errorf("%w: fact selector", ErrInvalid)
			}
			switch f.Kind {
			case ProductAction, Reading, Impression, LocalSemantic, SelfReport:
			default:
				return FeatureSpec{}, "", fmt.Errorf("%w: fact kind", ErrInvalid)
			}
		case "ontology_rule":
			if !token.MatchString(f.Rule) || f.Kind != "" || f.Predicate != "" || f.Mode != "count" || f.WindowSeconds != 0 {
				return FeatureSpec{}, "", fmt.Errorf("%w: ontology rule selector", ErrInvalid)
			}
		default:
			return FeatureSpec{}, "", fmt.Errorf("%w: feature source", ErrInvalid)
		}
		if f.Mode != "count" && f.Mode != "latest" {
			return FeatureSpec{}, "", fmt.Errorf("%w: feature mode", ErrInvalid)
		}
		if f.Mode == "count" {
			if f.Default != "0" || f.OOV != "" || len(f.Vocabulary) != 0 {
				return FeatureSpec{}, "", fmt.Errorf("%w: count default", ErrInvalid)
			}
		} else {
			if !token.MatchString(f.Default) || !token.MatchString(f.OOV) || f.Default == f.OOV || len(f.Vocabulary) == 0 || len(f.Vocabulary) > 4096 {
				return FeatureSpec{}, "", fmt.Errorf("%w: latest default or OOV", ErrInvalid)
			}
			f.Vocabulary = slices.Clone(f.Vocabulary)
			sort.Strings(f.Vocabulary)
			for j, v := range f.Vocabulary {
				if !token.MatchString(v) || v == f.OOV || (j > 0 && f.Vocabulary[j-1] == v) {
					return FeatureSpec{}, "", fmt.Errorf("%w: vocabulary", ErrInvalid)
				}
			}
		}
	}
	body, err := json.Marshal(spec)
	if err != nil {
		return FeatureSpec{}, "", err
	}
	sum := sha256.Sum256(body)
	return spec, hex.EncodeToString(sum[:]), nil
}

// PrepareFeatureSpec is the common contract boundary for a warehouse producer
// and the near-line consumer. Both must hash the same canonical rule set.
func PrepareFeatureSpec(input FeatureSpec) (FeatureSpec, string, error) {
	return canonicalFeatureSpec(input)
}

func featureEventKey(key EventKey) string                { return key.Producer + "\x00" + key.EventID }
func featureSourceKey(producer, partition string) string { return producer + "\x00" + partition }

// featureValues applies the same narrow count/latest qualification to a frozen
// row set. Batch SQL remains the authority for actual DWS jobs; this function
// is also the deterministic near-line rule and fixed fixture comparator.
func featureValues(spec FeatureSpec, facts []Fact, ontology *OntologyProjection, asOf, availableAt time.Time) ([]FeatureValue, error) {
	values := make([]FeatureValue, 0, len(spec.Features))
	for _, def := range spec.Features {
		value := FeatureValue{Name: def.Name, Value: def.Default, Missing: true}
		if def.Source == "ontology_rule" {
			if ontology == nil {
				return nil, ErrPending
			}
			for _, derived := range ontology.Derived {
				if derived.Rule == def.Rule {
					value.Value = strconv.FormatInt(derived.Value, 10)
					value.Missing = false
					break
				}
			}
			values = append(values, value)
			continue
		}
		var selected *Fact
		var count int64
		for i := range facts {
			f := &facts[i]
			if f.Status != "accepted" || f.Action == Retract || f.Kind != def.Kind || f.Predicate != def.Predicate ||
				f.OccurredAt.After(asOf) || f.ObservedAt.After(availableAt) {
				continue
			}
			if def.WindowSeconds > 0 && !f.OccurredAt.After(asOf.Add(-time.Duration(def.WindowSeconds)*time.Second)) {
				continue
			}
			count++
			if selected == nil || f.OccurredAt.After(selected.OccurredAt) ||
				(f.OccurredAt.Equal(selected.OccurredAt) && featureEventKey(f.EventKey) > featureEventKey(selected.EventKey)) {
				selected = f
			}
		}
		if def.Mode == "count" {
			value.Value = strconv.FormatInt(count, 10)
			value.Missing = count == 0
		} else if selected != nil {
			value.Value = selected.ValueRef
			value.Missing = false
			if !slices.Contains(def.Vocabulary, value.Value) {
				value.Value = def.OOV
				value.OOV = true
			}
		}
		values = append(values, value)
	}
	return values, nil
}

func canonicalFeatureBaseline(input FeatureBaseline, spec FeatureSpec, specHash string) (FeatureBaseline, string, error) {
	b := input
	if !b.Subject.valid() || b.Revision <= 0 || !token.MatchString(b.Generation) || b.SpecVersion != spec.Version || b.SpecHash != specHash ||
		b.AsOf.IsZero() || b.AvailableAt.IsZero() || b.AsOf.After(b.AvailableAt) || len(b.Watermarks) > 1024 || len(b.Contributions) > 100000 {
		return FeatureBaseline{}, "", fmt.Errorf("%w: baseline identity, version or time", ErrInvalid)
	}
	b.AsOf, b.AvailableAt = b.AsOf.UTC(), b.AvailableAt.UTC()
	b.Watermarks = slices.Clone(input.Watermarks)
	sort.Slice(b.Watermarks, func(i, j int) bool {
		return featureSourceKey(b.Watermarks[i].Producer, b.Watermarks[i].SourcePartition) < featureSourceKey(b.Watermarks[j].Producer, b.Watermarks[j].SourcePartition)
	})
	wms := map[string]int64{}
	seenSources := map[string]bool{}
	for _, w := range b.Watermarks {
		key := featureSourceKey(w.Producer, w.SourcePartition)
		if !token.MatchString(w.Producer) || !token.MatchString(w.SourcePartition) || w.ContiguousSequence < 0 ||
			w.MaxSeenSequence != w.ContiguousSequence || !w.Complete || seenSources[key] {
			return FeatureBaseline{}, "", fmt.Errorf("%w: baseline source watermark", ErrInvalid)
		}
		seenSources[key] = true
		wms[key] = w.ContiguousSequence
	}
	b.Contributions = slices.Clone(input.Contributions)
	sort.Slice(b.Contributions, func(i, j int) bool {
		return featureEventKey(b.Contributions[i].EventKey) < featureEventKey(b.Contributions[j].EventKey)
	})
	seen := map[string]bool{}
	for _, f := range b.Contributions {
		key := featureEventKey(f.EventKey)
		_, hash, _, err := normalized(f.Event, true)
		if err != nil || f.Subject != b.Subject || f.Status != "accepted" || f.Action == Retract ||
			f.NormalizedHash != hash || f.SourceSequence <= 0 || f.SourceSequence > wms[featureSourceKey(f.Producer, f.SourcePartition)] ||
			f.OccurredAt.After(b.AsOf) || f.ObservedAt.After(b.AvailableAt) || seen[key] {
			return FeatureBaseline{}, "", fmt.Errorf("%w: baseline contribution identity or coverage", ErrInvalid)
		}
		seen[key] = true
	}
	b.Values = slices.Clone(input.Values)
	factSpec := FeatureSpec{Version: spec.Version}
	for _, def := range spec.Features {
		if def.Source == "fact" {
			factSpec.Features = append(factSpec.Features, def)
		}
	}
	expected, err := featureValues(factSpec, b.Contributions, nil, b.AsOf, b.AvailableAt)
	if err != nil {
		return FeatureBaseline{}, "", err
	}
	if len(b.Values) == 0 {
		b.Values = []FeatureValue{}
	}
	if !reflect.DeepEqual(b.Values, expected) {
		return FeatureBaseline{}, "", fmt.Errorf("%w: baseline feature values", ErrInvalid)
	}
	body, err := json.Marshal(b)
	if err != nil {
		return FeatureBaseline{}, "", err
	}
	sum := sha256.Sum256(body)
	return b, hex.EncodeToString(sum[:]), nil
}

// mergeFeatureSnapshot reselects from the frozen baseline and the currently
// active tail. It never carries forward an old accumulated tail value.
func mergeFeatureSnapshot(spec FeatureSpec, specHash string, baseline *FeatureBaseline, active []Fact,
	watermarks []Watermark, stateVersion int64, ontology *OntologyProjection, asOf, availableAt time.Time) (FeatureSnapshot, error) {
	if asOf.IsZero() || availableAt.IsZero() || asOf.After(availableAt) || stateVersion < 0 {
		return FeatureSnapshot{}, ErrInvalid
	}
	snapshot := FeatureSnapshot{SpecVersion: spec.Version, SpecHash: specHash, StateVersion: stateVersion,
		AsOf: asOf.UTC(), AvailableAt: availableAt.UTC(), Watermarks: slices.Clone(watermarks), Tail: []EventKey{}, Values: []FeatureValue{}}
	if baseline == nil {
		snapshot.BaselineState = "absent"
	} else {
		snapshot.Subject = baseline.Subject
		snapshot.BaselineState = "accepted"
		snapshot.Generation, snapshot.Revision = baseline.Generation, baseline.Revision
		if baseline.SpecHash != specHash || baseline.SpecVersion != spec.Version || baseline.AsOf.After(asOf) || baseline.AvailableAt.After(availableAt) {
			return FeatureSnapshot{}, ErrConflict
		}
	}
	wm := map[string]int64{}
	for _, w := range watermarks {
		wm[featureSourceKey(w.Producer, w.SourcePartition)] = w.ContiguousSequence
	}
	covered := map[string]int64{}
	baseFacts := map[string]Fact{}
	if baseline != nil {
		for _, w := range baseline.Watermarks {
			key := featureSourceKey(w.Producer, w.SourcePartition)
			if w.ContiguousSequence > wm[key] {
				return FeatureSnapshot{}, fmt.Errorf("%w: baseline ahead of source", ErrConflict)
			}
			covered[key] = w.ContiguousSequence
		}
		for _, f := range baseline.Contributions {
			baseFacts[featureEventKey(f.EventKey)] = f
		}
	}
	selected := make([]Fact, 0, len(active))
	seen := map[string]bool{}
	for _, f := range active {
		key := featureEventKey(f.EventKey)
		if seen[key] || f.Status != "accepted" || f.Action == Retract {
			return FeatureSnapshot{}, ErrConflict
		}
		seen[key] = true
		if baseline == nil {
			selected = append(selected, f)
			snapshot.Tail = append(snapshot.Tail, f.EventKey)
			continue
		}
		if f.Subject != baseline.Subject {
			return FeatureSnapshot{}, ErrConflict
		}
		if f.SourceSequence > 0 && f.SourceSequence <= covered[featureSourceKey(f.Producer, f.SourcePartition)] {
			base, ok := baseFacts[key]
			if !ok || base.NormalizedHash != f.NormalizedHash {
				return FeatureSnapshot{}, fmt.Errorf("%w: covered fact missing from reversible baseline", ErrPending)
			}
			selected = append(selected, f)
			continue
		}
		if _, ok := baseFacts[key]; ok {
			return FeatureSnapshot{}, fmt.Errorf("%w: baseline contribution beyond watermark", ErrConflict)
		}
		selected = append(selected, f)
		snapshot.Tail = append(snapshot.Tail, f.EventKey)
	}
	sort.Slice(snapshot.Tail, func(i, j int) bool { return featureEventKey(snapshot.Tail[i]) < featureEventKey(snapshot.Tail[j]) })
	values, err := featureValues(spec, selected, ontology, asOf, availableAt)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	snapshot.Values = values
	if ontology != nil {
		snapshot.OntologyVersion = ontology.DefinitionVersion
		if ontology.NextChangeAt != nil {
			copy := ontology.NextChangeAt.UTC()
			snapshot.NextChangeAt = &copy
		}
	}
	for _, def := range spec.Features {
		if def.Source != "fact" || def.WindowSeconds == 0 {
			continue
		}
		for _, fact := range selected {
			if fact.Kind != def.Kind || fact.Predicate != def.Predicate || fact.OccurredAt.After(asOf) || fact.ObservedAt.After(availableAt) {
				continue
			}
			expires := fact.OccurredAt.Add(time.Duration(def.WindowSeconds) * time.Second)
			if !expires.After(asOf) {
				continue
			}
			if snapshot.NextChangeAt == nil || expires.Before(*snapshot.NextChangeAt) {
				snapshot.NextChangeAt = &expires
			}
		}
	}
	return snapshot, nil
}
