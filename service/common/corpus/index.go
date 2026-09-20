package corpus

type Profile struct {
	Lane        string `json:"lane"`
	Encoder     string `json:"encoder"`
	Tokenizer   string `json:"tokenizer"`
	Space       string `json:"space"`
	Dimensions  int    `json:"dimensions"`
	Mask        string `json:"mask,omitempty"`
	Aggregation string `json:"aggregation,omitempty"`
}

type IndexShard struct {
	Artifact Ref      `json:"artifact"`
	ChunkIDs []string `json:"chunk_ids"`
}

// LaneIndex describes exact immutable lane-owned shards, not the lane's internal
// representation format. The lane verifier must validate that format and run
// queries against these exact shards before a readiness manifest can be emitted.
type LaneIndex struct {
	SchemaVersion     int          `json:"schema_version"`
	BuildID           string       `json:"build_id"`
	Generation        int64        `json:"generation"`
	InputManifestHash string       `json:"input_manifest_hash"`
	ChunkManifest     Ref          `json:"chunk_manifest"`
	Profile           Profile      `json:"profile"`
	Shards            []IndexShard `json:"shards"`
}

type ProbeResult struct {
	QueryChunkID string   `json:"query_chunk_id"`
	CandidateIDs []string `json:"candidate_ids"`
}

type LaneProbe struct {
	SchemaVersion int           `json:"schema_version"`
	Lane          string        `json:"lane"`
	Index         Ref           `json:"index"`
	ChunkManifest Ref           `json:"chunk_manifest"`
	Results       []ProbeResult `json:"results"`
}

type LaneManifest struct {
	Profile     Profile `json:"profile"`
	Artifact    Ref     `json:"artifact"`
	ChunkCount  int64   `json:"chunk_count"`
	Shards      int     `json:"shards"`
	ProbePassed bool    `json:"probe_passed"`
	Probe       Ref     `json:"probe"`
}

// IndexManifest is the RTW H06 v1 wire shape. READY remains a content-domain
// result; it does not advance the product's active release pointer.
type IndexManifest struct {
	SchemaVersion     int            `json:"schema_version"`
	BuildID           string         `json:"build_id"`
	ReleaseID         string         `json:"release_id"`
	Generation        int64          `json:"generation"`
	InputManifestHash string         `json:"input_manifest_hash"`
	ChunkManifest     Ref            `json:"chunk_manifest"`
	ChunkCount        int64          `json:"chunk_count"`
	Lanes             []LaneManifest `json:"lanes"`
}
