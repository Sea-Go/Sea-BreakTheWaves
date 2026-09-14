package usermodel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"
)

// OntologyDefinition is an immutable, tenant-scoped interpretation of accepted
// user facts. This first slice deliberately supports only subject -> object
// relations and deterministic integer counts. It cannot execute product actions.
type OntologyDefinition struct {
	AuthorityID string             `json:"authority_id"`
	TenantID    string             `json:"tenant_id"`
	Version     int64              `json:"version"`
	Objects     []OntologyObject   `json:"objects"`
	Relations   []OntologyRelation `json:"relations"`
	Rules       []OntologyRule     `json:"rules"`
}

type OntologyObject struct {
	Name       string              `json:"name"`
	Attributes []OntologyAttribute `json:"attributes,omitempty"`
}

type OntologyAttribute struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // integer or string; only subject integers may be derived.
	Nullable bool   `json:"nullable"`
}

type OntologyRelation struct {
	Name          string       `json:"name"`
	ToObject      string       `json:"to_object"`
	Predicate     string       `json:"predicate"`
	Kind          SemanticKind `json:"kind"`
	Cardinality   string       `json:"cardinality"`    // one or many; outbound from subject.
	WindowSeconds int64        `json:"window_seconds"` // zero means no time cutoff.
}

// An input is relation.NAME or rule.NAME. A rule sums distinct target counts
// from relations and integer values from other rules. Missing inputs count zero.
type OntologyRule struct {
	Name            string   `json:"name"`
	OutputAttribute string   `json:"output_attribute"`
	Inputs          []string `json:"inputs"`
}

type OntologyEvidence struct {
	EventKey
	EvidenceRef     string    `json:"evidence_ref"`
	EvidenceHash    string    `json:"evidence_hash"`
	AcceptedVersion int64     `json:"accepted_version"`
	OccurredAt      time.Time `json:"occurred_at"`
}

type OntologyEdge struct {
	Relation string             `json:"relation"`
	ToObject string             `json:"to_object"`
	ToID     string             `json:"to_id"`
	Evidence []OntologyEvidence `json:"evidence"`
}

type OntologyDerived struct {
	Rule      string             `json:"rule"`
	Attribute string             `json:"attribute"`
	Value     int64              `json:"value"`
	Evidence  []OntologyEvidence `json:"evidence"`
}

type OntologyProjection struct {
	Subject           SubjectRef        `json:"subject_ref"`
	DefinitionVersion int64             `json:"definition_version"`
	StateVersion      int64             `json:"state_version"`
	AsOf              time.Time         `json:"as_of"`
	NextChangeAt      *time.Time        `json:"next_change_at,omitempty"`
	Edges             []OntologyEdge    `json:"edges"`
	Derived           []OntologyDerived `json:"derived"`
}

type OntologyObjectRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// RecomputeTargets are intentionally limited to the user side of C14.
type OntologyImpact struct {
	Subject           SubjectRef          `json:"subject_ref"`
	PreviousVersion   int64               `json:"previous_state_version"`
	StateVersion      int64               `json:"state_version"`
	DefinitionVersion int64               `json:"definition_version"`
	AffectedObjects   []OntologyObjectRef `json:"affected_objects"`
	AffectedRules     []string            `json:"affected_rules"`
	RecomputeTargets  []string            `json:"recompute_targets"`
	Replay            bool                `json:"replay"`
}

// canonicalOntologyDefinition validates every reference before publication and
// hashes a stable representation, so reordering declarations is an idempotent
// replay while a different body at the same version is a conflict.
func canonicalOntologyDefinition(input OntologyDefinition) (OntologyDefinition, string, []byte, error) {
	d := input
	if !token.MatchString(d.AuthorityID) || !token.MatchString(d.TenantID) || d.Version <= 0 ||
		len(d.Objects) == 0 || len(d.Objects) > 64 || len(d.Relations) > 128 || len(d.Rules) > 128 {
		return OntologyDefinition{}, "", nil, fmt.Errorf("%w: ontology scope or size", ErrInvalid)
	}
	d.Objects = slices.Clone(input.Objects)
	d.Relations = slices.Clone(input.Relations)
	d.Rules = slices.Clone(input.Rules)
	if d.Relations == nil {
		d.Relations = []OntologyRelation{}
	}
	if d.Rules == nil {
		d.Rules = []OntologyRule{}
	}
	objects := map[string]map[string]string{}
	for i := range d.Objects {
		o := &d.Objects[i]
		if !token.MatchString(o.Name) || objects[o.Name] != nil || len(o.Attributes) > 256 {
			return OntologyDefinition{}, "", nil, fmt.Errorf("%w: duplicate or invalid ontology object", ErrInvalid)
		}
		o.Attributes = slices.Clone(o.Attributes)
		attrs := map[string]string{}
		for _, a := range o.Attributes {
			if !token.MatchString(a.Name) || attrs[a.Name] != "" || (a.Type != "integer" && a.Type != "string") {
				return OntologyDefinition{}, "", nil, fmt.Errorf("%w: ontology attribute type", ErrInvalid)
			}
			attrs[a.Name] = a.Type
		}
		sort.Slice(o.Attributes, func(i, j int) bool { return o.Attributes[i].Name < o.Attributes[j].Name })
		objects[o.Name] = attrs
	}
	if objects["subject"] == nil {
		return OntologyDefinition{}, "", nil, fmt.Errorf("%w: subject object required", ErrInvalid)
	}
	relations := map[string]struct{}{}
	for _, r := range d.Relations {
		if !token.MatchString(r.Name) || !token.MatchString(r.Predicate) || r.ToObject == "subject" ||
			objects[r.ToObject] == nil || r.WindowSeconds < 0 || r.WindowSeconds > int64((365*24*time.Hour)/time.Second) ||
			(r.Cardinality != "one" && r.Cardinality != "many") {
			return OntologyDefinition{}, "", nil, fmt.Errorf("%w: ontology relation scope or cardinality", ErrInvalid)
		}
		switch r.Kind {
		case ProductAction, Reading, Impression, LocalSemantic, SelfReport:
		default:
			return OntologyDefinition{}, "", nil, fmt.Errorf("%w: ontology relation kind", ErrInvalid)
		}
		if _, ok := relations[r.Name]; ok {
			return OntologyDefinition{}, "", nil, fmt.Errorf("%w: duplicate ontology relation", ErrInvalid)
		}
		relations[r.Name] = struct{}{}
	}
	rules := map[string]OntologyRule{}
	outputAttrs := map[string]struct{}{}
	for i := range d.Rules {
		r := &d.Rules[i]
		if !token.MatchString(r.Name) || !token.MatchString(r.OutputAttribute) ||
			objects["subject"][r.OutputAttribute] != "integer" || len(r.Inputs) == 0 || len(r.Inputs) > 128 {
			return OntologyDefinition{}, "", nil, fmt.Errorf("%w: ontology rule output type", ErrInvalid)
		}
		if _, exists := rules[r.Name]; exists {
			return OntologyDefinition{}, "", nil, fmt.Errorf("%w: duplicate ontology rule", ErrInvalid)
		}
		if _, exists := outputAttrs[r.OutputAttribute]; exists {
			return OntologyDefinition{}, "", nil, fmt.Errorf("%w: duplicate ontology rule output", ErrInvalid)
		}
		outputAttrs[r.OutputAttribute] = struct{}{}
		r.Inputs = slices.Clone(r.Inputs)
		sort.Strings(r.Inputs)
		for j, in := range r.Inputs {
			if j > 0 && in == r.Inputs[j-1] {
				return OntologyDefinition{}, "", nil, fmt.Errorf("%w: duplicate ontology rule input", ErrInvalid)
			}
		}
		rules[r.Name] = *r
	}
	visited := map[string]uint8{}
	var visit func(string) error
	visit = func(name string) error {
		if visited[name] == 1 {
			return fmt.Errorf("%w: ontology rule dependency cycle", ErrInvalid)
		}
		if visited[name] == 2 {
			return nil
		}
		r, ok := rules[name]
		if !ok {
			return fmt.Errorf("%w: ontology rule reference", ErrInvalid)
		}
		visited[name] = 1
		for _, in := range r.Inputs {
			parts := strings.SplitN(in, ".", 2)
			if len(parts) != 2 || !token.MatchString(parts[1]) {
				return fmt.Errorf("%w: ontology input reference", ErrInvalid)
			}
			switch parts[0] {
			case "relation":
				if _, ok := relations[parts[1]]; !ok {
					return fmt.Errorf("%w: unknown ontology relation", ErrInvalid)
				}
			case "rule":
				if err := visit(parts[1]); err != nil {
					return err
				}
			default:
				return fmt.Errorf("%w: unsupported ontology rule input", ErrInvalid)
			}
		}
		visited[name] = 2
		return nil
	}
	for name := range rules {
		if err := visit(name); err != nil {
			return OntologyDefinition{}, "", nil, err
		}
	}
	sort.Slice(d.Objects, func(i, j int) bool { return d.Objects[i].Name < d.Objects[j].Name })
	sort.Slice(d.Relations, func(i, j int) bool { return d.Relations[i].Name < d.Relations[j].Name })
	sort.Slice(d.Rules, func(i, j int) bool { return d.Rules[i].Name < d.Rules[j].Name })
	body, err := json.Marshal(d)
	if err != nil {
		return OntologyDefinition{}, "", nil, err
	}
	sum := sha256.Sum256(body)
	return d, hex.EncodeToString(sum[:]), body, nil
}

// BuildOntologyProjection is pure: pinned facts, definition and as-of time
// always produce the same edges, evidence and derived values after a restart.
func BuildOntologyProjection(d OntologyDefinition, facts Projection, asOf time.Time) (OntologyProjection, error) {
	canonical, _, _, err := canonicalOntologyDefinition(d)
	if err != nil || !facts.Subject.valid() || facts.Subject.AuthorityID != d.AuthorityID ||
		facts.Subject.TenantID != d.TenantID || facts.StateVersion < 0 || asOf.IsZero() {
		return OntologyProjection{}, ErrInvalid
	}
	d, asOf = canonical, asOf.UTC()
	p := OntologyProjection{Subject: facts.Subject, DefinitionVersion: d.Version, StateVersion: facts.StateVersion,
		AsOf: asOf, Edges: []OntologyEdge{}, Derived: []OntologyDerived{}}
	byRelation := map[string]map[string]*OntologyEdge{}
	for _, r := range d.Relations {
		byRelation[r.Name] = map[string]*OntologyEdge{}
		for _, f := range facts.Active {
			if f.Status != "accepted" || f.Subject != facts.Subject || f.Kind != r.Kind || f.Predicate != r.Predicate ||
				f.ValueRef == "" || f.AcceptedVersion <= 0 || f.AcceptedVersion > facts.StateVersion ||
				!f.EventKey.valid() || !token.MatchString(f.EvidenceRef) || !digest.MatchString(f.EvidenceHash) {
				continue
			}
			if f.OccurredAt.After(asOf) {
				p.NextChangeAt = sooner(p.NextChangeAt, f.OccurredAt)
				continue
			}
			if r.WindowSeconds > 0 {
				expires := f.OccurredAt.Add(time.Duration(r.WindowSeconds) * time.Second)
				if !expires.After(asOf) {
					continue
				}
				p.NextChangeAt = sooner(p.NextChangeAt, expires)
			}
			e := byRelation[r.Name][f.ValueRef]
			if e == nil {
				e = &OntologyEdge{Relation: r.Name, ToObject: r.ToObject, ToID: f.ValueRef, Evidence: []OntologyEvidence{}}
				byRelation[r.Name][f.ValueRef] = e
			}
			e.Evidence = append(e.Evidence, OntologyEvidence{EventKey: f.EventKey, EvidenceRef: f.EvidenceRef,
				EvidenceHash: f.EvidenceHash, AcceptedVersion: f.AcceptedVersion, OccurredAt: f.OccurredAt.UTC()})
		}
		if r.Cardinality == "one" && len(byRelation[r.Name]) > 1 {
			var best *OntologyEdge
			for _, e := range byRelation[r.Name] {
				sortEvidence(e.Evidence)
				if best == nil || evidenceLater(e.Evidence[len(e.Evidence)-1], best.Evidence[len(best.Evidence)-1]) {
					best = e
				}
			}
			byRelation[r.Name] = map[string]*OntologyEdge{best.ToID: best}
		}
		for _, e := range byRelation[r.Name] {
			sortEvidence(e.Evidence)
			p.Edges = append(p.Edges, *e)
		}
	}
	sort.Slice(p.Edges, func(i, j int) bool {
		if p.Edges[i].Relation != p.Edges[j].Relation {
			return p.Edges[i].Relation < p.Edges[j].Relation
		}
		return p.Edges[i].ToID < p.Edges[j].ToID
	})
	derived := map[string]OntologyDerived{}
	rules := map[string]OntologyRule{}
	for _, r := range d.Rules {
		rules[r.Name] = r
	}
	var eval func(string) OntologyDerived
	eval = func(name string) OntologyDerived {
		if got, ok := derived[name]; ok {
			return got
		}
		r := rules[name]
		v := OntologyDerived{Rule: name, Attribute: r.OutputAttribute, Evidence: []OntologyEvidence{}}
		for _, input := range r.Inputs {
			parts := strings.SplitN(input, ".", 2)
			if parts[0] == "relation" {
				for _, edge := range p.Edges {
					if edge.Relation == parts[1] {
						v.Value++
						v.Evidence = append(v.Evidence, edge.Evidence...)
					}
				}
			} else {
				parent := eval(parts[1])
				v.Value += parent.Value
				v.Evidence = append(v.Evidence, parent.Evidence...)
			}
		}
		v.Evidence = uniqueEvidence(v.Evidence)
		derived[name] = v
		return v
	}
	for _, r := range d.Rules {
		p.Derived = append(p.Derived, eval(r.Name))
	}
	return p, nil
}

func sooner(previous *time.Time, next time.Time) *time.Time {
	if previous == nil || next.Before(*previous) {
		t := next.UTC()
		return &t
	}
	return previous
}

func evidenceLater(a, b OntologyEvidence) bool {
	if !a.OccurredAt.Equal(b.OccurredAt) {
		return a.OccurredAt.After(b.OccurredAt)
	}
	if a.Producer != b.Producer {
		return a.Producer > b.Producer
	}
	return a.EventID > b.EventID
}

func sortEvidence(e []OntologyEvidence) {
	sort.Slice(e, func(i, j int) bool {
		if !e[i].OccurredAt.Equal(e[j].OccurredAt) {
			return e[i].OccurredAt.Before(e[j].OccurredAt)
		}
		if e[i].Producer != e[j].Producer {
			return e[i].Producer < e[j].Producer
		}
		return e[i].EventID < e[j].EventID
	})
}

func uniqueEvidence(e []OntologyEvidence) []OntologyEvidence {
	sortEvidence(e)
	out := []OntologyEvidence{}
	for _, v := range e {
		if len(out) == 0 || out[len(out)-1].EventKey != v.EventKey {
			out = append(out, v)
		}
	}
	return out
}

func ontologyImpact(before, after OntologyProjection, replay bool) OntologyImpact {
	impact := OntologyImpact{Subject: after.Subject, PreviousVersion: before.StateVersion,
		StateVersion: after.StateVersion, DefinitionVersion: after.DefinitionVersion,
		AffectedObjects: []OntologyObjectRef{}, AffectedRules: []string{}, RecomputeTargets: []string{}, Replay: replay}
	if replay {
		return impact
	}
	changedDefinition := before.DefinitionVersion != after.DefinitionVersion
	oldEdges, newEdges := map[string]OntologyEdge{}, map[string]OntologyEdge{}
	for _, e := range before.Edges {
		oldEdges[e.Relation+"\x00"+e.ToObject+"\x00"+e.ToID] = e
	}
	for _, e := range after.Edges {
		newEdges[e.Relation+"\x00"+e.ToObject+"\x00"+e.ToID] = e
	}
	objects, changedRelations := map[OntologyObjectRef]struct{}{}, map[string]struct{}{}
	for k, old := range oldEdges {
		if next, ok := newEdges[k]; changedDefinition || !ok || !reflect.DeepEqual(old, next) {
			objects[OntologyObjectRef{Type: old.ToObject, ID: old.ToID}] = struct{}{}
			changedRelations[old.Relation] = struct{}{}
		}
	}
	for k, next := range newEdges {
		if old, ok := oldEdges[k]; changedDefinition || !ok || !reflect.DeepEqual(old, next) {
			objects[OntologyObjectRef{Type: next.ToObject, ID: next.ToID}] = struct{}{}
			changedRelations[next.Relation] = struct{}{}
		}
	}
	oldRules := map[string]OntologyDerived{}
	for _, v := range before.Derived {
		oldRules[v.Rule] = v
	}
	for _, v := range after.Derived {
		if old, ok := oldRules[v.Rule]; changedDefinition || !ok || !reflect.DeepEqual(old, v) {
			impact.AffectedRules = append(impact.AffectedRules, v.Rule)
		}
		delete(oldRules, v.Rule)
	}
	for name := range oldRules {
		impact.AffectedRules = append(impact.AffectedRules, name)
	}
	if len(changedRelations) > 0 || len(impact.AffectedRules) > 0 || changedDefinition {
		objects[OntologyObjectRef{Type: "subject", ID: after.Subject.SubjectID}] = struct{}{}
		impact.RecomputeTargets = []string{"user_state", "user_features"}
	}
	for o := range objects {
		impact.AffectedObjects = append(impact.AffectedObjects, o)
	}
	sort.Slice(impact.AffectedObjects, func(i, j int) bool {
		if impact.AffectedObjects[i].Type != impact.AffectedObjects[j].Type {
			return impact.AffectedObjects[i].Type < impact.AffectedObjects[j].Type
		}
		return impact.AffectedObjects[i].ID < impact.AffectedObjects[j].ID
	})
	sort.Strings(impact.AffectedRules)
	return impact
}
