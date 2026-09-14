// Code generated from RideTheWind api/knowledge.api via goctl types. DO NOT EDIT.
package ridethewind

type AcceptBuildReq struct {
	BuildId           string `json:"-"`
	Generation        int64  `json:"generation"`
	AttemptId         string `json:"attempt_id"`
	LeaseEpoch        int64  `json:"lease_epoch"`
	CancelVersion     int64  `json:"cancel_version"`
	ManifestHash      string `json:"manifest_hash"`
	IndexManifestRef  string `json:"index_manifest_ref,omitempty"`
	IndexManifestHash string `json:"index_manifest_hash,omitempty"`
	State             string `json:"state"`
	ErrorCode         string `json:"error_code,omitempty"`
}

type AcceptCompileReq struct {
	State         string      `json:"state,omitempty"`
	ErrorCode     string      `json:"error_code,omitempty"`
	CompileId     string      `json:"-"`
	Generation    int64       `json:"generation"`
	AttemptId     string      `json:"attempt_id"`
	LeaseEpoch    int64       `json:"lease_epoch"`
	CancelVersion int64       `json:"cancel_version"`
	InputHash     string      `json:"input_hash"`
	ObjectKey     string      `json:"object_key,omitempty"`
	ContentHash   string      `json:"content_hash,omitempty"`
	Title         string      `json:"title,omitempty"`
	SourceRefs    []SourceRef `json:"source_refs,omitempty"`
}

type Build struct {
	LeaseExpiresAt    string `json:"lease_expires_at"`
	BuildId           string `json:"build_id"`
	ReleaseId         string `json:"release_id"`
	ModuleId          string `json:"module_id"`
	ManifestHash      string `json:"manifest_hash"`
	Generation        int64  `json:"generation"`
	State             string `json:"state"`
	AttemptId         string `json:"attempt_id"`
	LeaseEpoch        int64  `json:"lease_epoch"`
	CancelVersion     int64  `json:"cancel_version"`
	IndexManifestRef  string `json:"index_manifest_ref"`
	IndexManifestHash string `json:"index_manifest_hash"`
	ErrorCode         string `json:"error_code"`
}

type ClaimBuildReq struct {
	LeaseExpiresAt string `json:"lease_expires_at"`
	BuildId        string `json:"-"`
	Generation     int64  `json:"generation"`
	AttemptId      string `json:"attempt_id"`
	LeaseEpoch     int64  `json:"lease_epoch"`
	CancelVersion  int64  `json:"cancel_version"`
	ManifestHash   string `json:"manifest_hash"`
}

type ClaimCompileReq struct {
	LeaseExpiresAt string `json:"lease_expires_at"`
	CompileId      string `json:"-"`
	Generation     int64  `json:"generation"`
	AttemptId      string `json:"attempt_id"`
	LeaseEpoch     int64  `json:"lease_epoch"`
	CancelVersion  int64  `json:"cancel_version"`
	InputHash      string `json:"input_hash"`
}

type Compile struct {
	ErrorCode         string   `json:"error_code"`
	LeaseExpiresAt    string   `json:"lease_expires_at"`
	CompileId         string   `json:"compile_id"`
	ModuleId          string   `json:"module_id"`
	PageId            string   `json:"page_id"`
	BaseRevisionId    string   `json:"base_revision_id"`
	SourceRevisionIds []string `json:"source_revision_ids"`
	Guidance          string   `json:"guidance"`
	InputHash         string   `json:"input_hash"`
	State             string   `json:"state"`
	Generation        int64    `json:"generation"`
	AttemptId         string   `json:"attempt_id"`
	LeaseEpoch        int64    `json:"lease_epoch"`
	CancelVersion     int64    `json:"cancel_version"`
	RevisionId        string   `json:"revision_id"`
	ResultHash        string   `json:"result_hash"`
}

type Release struct {
	ReleaseId         string             `json:"release_id"`
	ModuleId          string             `json:"module_id"`
	Ordinal           int64              `json:"ordinal"`
	SourceRevisionIds []string           `json:"source_revision_ids"`
	WikiRevisionIds   []string           `json:"wiki_revision_ids"`
	ChunkingProfile   string             `json:"chunking_profile"`
	RetrievalProfiles []RetrievalProfile `json:"retrieval_profiles"`
	ManifestHash      string             `json:"manifest_hash"`
	ManifestRef       string             `json:"manifest_ref"`
	CreatedAt         string             `json:"created_at"`
}

type RetrievalProfile struct {
	Lane        string `json:"lane"`
	Encoder     string `json:"encoder"`
	Tokenizer   string `json:"tokenizer"`
	Space       string `json:"space"`
	Dimensions  int    `json:"dimensions"`
	Mask        string `json:"mask,omitempty"`
	Aggregation string `json:"aggregation,omitempty"`
}

type Revision struct {
	RevisionId     string      `json:"revision_id"`
	ModuleId       string      `json:"module_id"`
	EntityId       string      `json:"entity_id"`
	Kind           string      `json:"kind"`
	BaseRevisionId string      `json:"base_revision_id"`
	Title          string      `json:"title"`
	MediaType      string      `json:"media_type"`
	ObjectKey      string      `json:"object_key"`
	ContentHash    string      `json:"content_hash"`
	Content        string      `json:"content,omitempty"`
	SourceRefs     []SourceRef `json:"source_refs"`
	Provenance     string      `json:"provenance"`
	CreatedBy      string      `json:"created_by"`
	CreatedAt      string      `json:"created_at"`
	Withdrawn      bool        `json:"withdrawn"`
}

type SourceRef struct {
	RevisionId string `json:"revision_id"`
	Locator    string `json:"locator"`
}
