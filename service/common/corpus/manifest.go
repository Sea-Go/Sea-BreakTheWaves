// Package corpus contains immutable content and location values shared by
// content construction and the independent retrieval lanes.
package corpus

type Ref struct {
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
}

// Revision is a fixed input, never an instruction to load the current head.
type Revision struct {
	RevisionID string `json:"revision_id"`
	ModuleID   string `json:"module_id"`
	EntityID   string `json:"entity_id"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	MediaType  string `json:"media_type"`
	Object     Ref    `json:"object"`
	Content    string `json:"content"`
}

// Location identifies a paragraph in the original revision, plus a precise
// span in its normalized text. Original byte bounds retain the unmodified
// paragraph, including whitespace; normalized rune bounds select the chunk.
type Location struct {
	Locator             string `json:"locator"`
	OriginalByteStart   int    `json:"original_byte_start"`
	OriginalByteEnd     int    `json:"original_byte_end"`
	NormalizedRuneStart int    `json:"normalized_rune_start"`
	NormalizedRuneEnd   int    `json:"normalized_rune_end"`
}

type Chunk struct {
	ID          string   `json:"chunk_id"`
	RevisionID  string   `json:"revision_id"`
	ContentID   string   `json:"content_id"`
	SourceKind  string   `json:"source_kind"`
	Original    Ref      `json:"original"`
	Location    Location `json:"location"`
	Text        string   `json:"text"`
	TextHash    string   `json:"text_hash"`
	EncodingKey string   `json:"encoding_key"`
	DuplicateOf string   `json:"duplicate_of,omitempty"`
	PreviousID  string   `json:"previous_id,omitempty"`
	NextID      string   `json:"next_id,omitempty"`
	Required    bool     `json:"required"`
}

type Input struct {
	RevisionID string `json:"revision_id"`
	ContentID  string `json:"content_id"`
	SourceKind string `json:"source_kind"`
	Original   Ref    `json:"original"`
	ChunkCount int    `json:"chunk_count"`
}

type ChunkManifest struct {
	SchemaVersion     int     `json:"schema_version"`
	ModuleID          string  `json:"module_id"`
	ReleaseID         string  `json:"release_id"`
	InputManifestHash string  `json:"input_manifest_hash"`
	Profile           string  `json:"profile"`
	ParserVersion     string  `json:"parser_version"`
	ChunkerVersion    string  `json:"chunker_version"`
	ChunkSize         int     `json:"chunk_size"`
	Overlap           int     `json:"overlap"`
	Inputs            []Input `json:"inputs"`
	Chunks            []Chunk `json:"chunks"`
}
