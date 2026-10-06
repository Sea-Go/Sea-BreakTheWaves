// Package item contains shared text-item contracts, not user-profile storage.
package item

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalid     = errors.New("invalid input")
	ErrConflict    = errors.New("revision conflict")
	ErrNotFound    = errors.New("not found")
	ErrUnavailable = errors.New("capability unavailable")
	ErrStale       = errors.New("obsolete revision or worker attempt")
)

type Node struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Label      string         `json:"label"`
	Attributes map[string]any `json:"attributes,omitempty"`
}
type Edge struct {
	From       string         `json:"from"`
	To         string         `json:"to"`
	Type       string         `json:"type"`
	Attributes map[string]any `json:"attributes,omitempty"`
}
type UserVector struct {
	Space  string    `json:"space"`
	Values []float32 `json:"values"`
}
type Ontology struct {
	UserID     string         `json:"user_id"`
	Nodes      []Node         `json:"nodes"`
	Relations  []Edge         `json:"relations"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Vector     *UserVector    `json:"vector,omitempty"`
}
type Request struct {
	RequestID   string    `json:"request_id"`
	Ontology    *Ontology `json:"ontology"`
	Scenario    string    `json:"scenario"`
	SearchLevel string    `json:"search_level,omitempty"`
	Query       string    `json:"query,omitempty"`
	Context     string    `json:"context,omitempty"`
	Limit       int       `json:"limit"`
}
type Location struct {
	RevisionID string `json:"revision_id"`
	SectionID  string `json:"section_id"`
	Start      int64  `json:"start"`
	End        int64  `json:"end"`
}
type Citation struct {
	ID       string   `json:"id"`
	ItemID   string   `json:"item_id"`
	Title    string   `json:"title"`
	Location Location `json:"location"`
	Quote    string   `json:"quote"`
	URL      string   `json:"url"`
}
type Item struct {
	Relations   []Relation         `json:"relations,omitempty"`
	Language    string             `json:"language,omitempty"`
	ID          string             `json:"id"`
	RootID      string             `json:"root_id"`
	Kind        string             `json:"kind"`
	RootKind    string             `json:"root_kind"`
	SourceID    string             `json:"source_id"`
	RevisionID  string             `json:"revision_id"`
	Title       string             `json:"title"`
	Summary     string             `json:"summary"`
	SourceURL   string             `json:"source_url,omitempty"`
	DuplicateID string             `json:"duplicate_id,omitempty"`
	Features    map[string]float64 `json:"features,omitempty"`
	Topics      []string           `json:"topics,omitempty"`
	Location    Location           `json:"location"`
}
type Hit struct {
	ModelFeatures []float64          `json:"-"`
	MultiLocation *Location          `json:"-"`
	Item          Item               `json:"item"`
	Score         float64            `json:"score"`
	Scores        map[string]float64 `json:"scores"`
	Lanes         []string           `json:"lanes"`
}
type PlanStep struct {
	Action      string   `json:"action"`
	CitationIDs []string `json:"citation_ids"`
}
type Plan struct {
	Name  string     `json:"name"`
	Goal  string     `json:"goal"`
	Steps []PlanStep `json:"steps"`
}
type Result struct {
	RequestID string     `json:"request_id"`
	Status    string     `json:"status"`
	Path      []string   `json:"path"`
	Hits      []Hit      `json:"hits"`
	Answer    string     `json:"answer,omitempty"`
	Plan      *Plan      `json:"plan,omitempty"`
	Citations []Citation `json:"citations"`
	Warnings  []string   `json:"warnings,omitempty"`
}
type StreamEvent struct {
	Type      string  `json:"type"`
	RequestID string  `json:"request_id"`
	Delta     string  `json:"delta,omitempty"`
	Result    *Result `json:"result,omitempty"`
	Error     string  `json:"error,omitempty"`
}
type SeekPoint struct {
	Rune int64 `json:"rune"`
	Byte int64 `json:"byte"`
}
type Navigation struct {
	ID        string `json:"id"`
	ParentID  string `json:"parent_id"`
	Title     string `json:"title"`
	Href      string `json:"href"`
	SectionID string `json:"section_id"`
	Start     int64  `json:"start"`
}
type Section struct {
	Anchors   map[string]int64 `json:"anchors,omitempty"`
	Seek      []SeekPoint      `json:"seek,omitempty"`
	ID        string           `json:"id"`
	ParentID  string           `json:"parent_id,omitempty"`
	Order     int              `json:"order"`
	Title     string           `json:"title"`
	Path      string           `json:"path"`
	ObjectKey string           `json:"object_key"`
	Bytes     int64            `json:"bytes"`
	Runes     int64            `json:"runes"`
}
type Relation struct {
	From     string   `json:"from"`
	To       string   `json:"to"`
	Type     string   `json:"type"`
	Location Location `json:"location"`
}
type Revision struct {
	OccurredAt         time.Time `json:"occurred_at"`
	ID                 string    `json:"id"`
	SourceID           string    `json:"source_id"`
	RootID             string    `json:"root_id"`
	Number             int64     `json:"revision"`
	Kind               string    `json:"kind"`
	Title              string    `json:"title"`
	MediaType          string    `json:"media_type"`
	FileKey            string    `json:"-"`
	TextKey            string    `json:"-"`
	SourceURL          string    `json:"source_url"`
	State              string    `json:"state"`
	Error              string    `json:"error,omitempty"`
	Bytes              int64     `json:"bytes"`
	ParsedSections     int       `json:"parsed_sections"`
	UnderstoodSections int       `json:"understood_sections"`
	CreatedAt          time.Time `json:"created_at"`
}
type ContentEvent struct {
	Producer   string    `json:"producer"`
	EventID    string    `json:"event_id"`
	SourceID   string    `json:"source_id"`
	Revision   int64     `json:"revision"`
	Operation  string    `json:"operation"`
	SourceURL  string    `json:"source_url,omitempty"`
	MediaType  string    `json:"media_type"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title"`
	Body       string    `json:"body,omitempty"`
	ObjectRef  string    `json:"object_ref,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}
type Admission struct {
	SourceID   string `json:"source_id"`
	ItemID     string `json:"item_id"`
	RevisionID string `json:"revision_id"`
	JobID      string `json:"job_id,omitempty"`
	Duplicate  bool   `json:"duplicate"`
	Obsolete   bool   `json:"obsolete"`
}
type Job struct {
	ParsedSections     int             `json:"parsed_sections"`
	UnderstoodSections int             `json:"understood_sections"`
	ID                 string          `json:"id"`
	RevisionID         string          `json:"revision_id"`
	Kind               string          `json:"kind"`
	Stage              string          `json:"stage"`
	State              string          `json:"state"`
	Attempt            int             `json:"attempt"`
	Token              string          `json:"-"`
	LeaseUntil         time.Time       `json:"lease_until"`
	NextRun            time.Time       `json:"next_run"`
	Error              string          `json:"error,omitempty"`
	Payload            json.RawMessage `json:"-"`
}

// Retrieval is the model and index wiring of the currently deployed process.
// Changes to this wiring are deployed as one Git-owned application change.
type Retrieval struct {
	Match           string `json:"match,optional,omitempty"`
	MatchDimensions int    `json:"match_dimensions,optional,omitempty"`
	Model           string `json:"model"`
	Space           string `json:"space"`
	Dimensions      int    `json:"dimensions"`
	SearchRanker    string `json:"search_ranker,optional,omitempty"`
	RecommendRanker string `json:"recommend_ranker,optional,omitempty"`
	UserSpace       string `json:"user_space,optional,omitempty"`
	ItemTower       string `json:"item_tower,optional,omitempty"`
	Dense           string `json:"dense"`
	Sparse          string `json:"sparse"`
	Multi           string `json:"multi"`
}
type Scenario struct {
	Kinds            []string `json:"kinds"`
	Delivery         string   `json:"delivery"`
	CandidateLimit   int      `json:"candidate_limit"`
	Limit            int      `json:"limit"`
	RRF              int      `json:"rrf"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
	MaxEvidenceBytes int      `json:"max_evidence_bytes"`
	Prompt           string   `json:"prompt"`
}
type Behavior struct {
	RootID         string    `json:"root_id,omitempty"`
	DuplicateID    string    `json:"duplicate_id,omitempty"`
	SourceRevision string    `json:"source_revision,omitempty"`
	EventID        string    `json:"event_id"`
	RequestID      string    `json:"request_id"`
	UserID         string    `json:"user_id"`
	ItemID         string    `json:"item_id"`
	Scenario       string    `json:"scenario"`
	Type           string    `json:"type"`
	Position       int       `json:"position"`
	OccurredAt     time.Time `json:"occurred_at"`
	Features       []float64 `json:"features,omitempty"`
	LabelSource    string    `json:"label_source,omitempty"`
}
type Sample struct {
	CandidateID         string    `json:"candidate_id,omitempty"`
	CandidateGroup      string    `json:"candidate_group,omitempty"`
	Negatives           []string  `json:"negatives,omitempty"`
	Exposed             bool      `json:"exposed,omitempty"`
	ObservationComplete bool      `json:"observation_complete,omitempty"`
	UserSpace           string    `json:"user_space,omitempty"`
	UserEmbedding       []float32 `json:"user_embedding,omitempty"`
	ItemEmbedding       []float32 `json:"item_embedding,omitempty"`
	Query               string    `json:"query"`
	Positive            string    `json:"positive"`
	RootID              string    `json:"root_id"`
	DuplicateID         string    `json:"duplicate_id"`
	SourceRevision      string    `json:"source_revision"`
	LabelSource         string    `json:"label_source"`
	Features            []float64 `json:"features,omitempty"`
	Label               float64   `json:"label"`
	Group               string    `json:"group"`
}

// Reader is the bounded read contract used by online domains.
type Reader interface {
	Resolve(context.Context, []string, []string) (map[string]Item, error)
	Read(context.Context, Location, int64) (string, error)
}
