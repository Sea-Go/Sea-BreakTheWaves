// Package three_lane_parity checks frozen BGE-M3 outputs against the existing
// BTW exact retrieval lanes. It is an acceptance adapter, not a new retriever.
package parity

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/multivector"
)

const RepresentationSchema = "sea.search.three-lane-representations.v1"

var ErrArtifact = errors.New("frozen three-lane representation artifact mismatch")

type LaneProfile struct {
	Model                  string                  `json:"model"`
	OutputContract         string                  `json:"output_contract"`
	RepresentationSpace    string                  `json:"representation_space"`
	RepresentationContract representation.Contract `json:"representation_contract"`
	MaxInputTokens         int                     `json:"max_input_tokens"`
	MaxBatch               int                     `json:"max_batch"`
}

type ShardRef struct {
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Rows      int    `json:"rows"`
}

type Manifest struct {
	SchemaVersion         string                 `json:"schema_version"`
	Status                string                 `json:"status"`
	DatasetID             string                 `json:"dataset_id"`
	DatasetManifestSHA256 string                 `json:"dataset_manifest_sha256"`
	DatasetRevision       int                    `json:"dataset_revision"`
	SourceRowCount        int                    `json:"source_row_count"`
	WarehouseGeneration   string                 `json:"warehouse_generation"`
	DataKind              string                 `json:"data_kind"`
	ModelRepository       string                 `json:"model_repository"`
	ModelRevision         string                 `json:"model_revision"`
	ModelLockSHA256       string                 `json:"model_lock_sha256"`
	ProfilesSHA256        string                 `json:"profiles_sha256"`
	TokenizerID           string                 `json:"tokenizer_id"`
	Lanes                 map[string]LaneProfile `json:"lanes"`
	Shards                []ShardRef             `json:"shards"`
	QueryCount            int                    `json:"query_count"`
	ChunkCount            int                    `json:"chunk_count"`
}

type Representations struct {
	Dense       *representation.DenseValues  `json:"dense"`
	Sparse      *representation.SparseValues `json:"sparse"`
	TokenMatrix *representation.TokenValues  `json:"token_matrix"`
}

type QueryRow struct {
	QueryID                string          `json:"query_id"`
	QueryFamilyID          string          `json:"query_family_id"`
	NearDuplicateClusterID string          `json:"near_duplicate_cluster_id"`
	Split                  string          `json:"split"`
	Text                   string          `json:"text"`
	TextSHA256             string          `json:"text_sha256"`
	Representations        Representations `json:"representations"`
}

type ChunkRow struct {
	DocumentID         string          `json:"document_id"`
	DocumentRevision   string          `json:"document_revision"`
	ChunkID            string          `json:"chunk_id"`
	ChunkKey           string          `json:"chunk_key"`
	Text               string          `json:"text"`
	TextSHA256         string          `json:"text_sha256"`
	ContentAvailableAt string          `json:"content_available_at"`
	Representations    Representations `json:"representations"`
}

type Frozen struct {
	ManifestSHA256 string
	Manifest       Manifest
	Queries        []QueryRow
	Chunks         []ChunkRow
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func exactJSON(raw []byte, out any) error {
	if err := representation.Decode(raw, out); err != nil {
		return fmt.Errorf("%w: strict JSON: %v", ErrArtifact, err)
	}
	return nil
}

// LoadFrozen verifies content-addressed manifest/shards and model/profile
// locks before the Go lanes see a vector. It checks original Python JSONL
// byte hashes; re-encoding floats as RFC8785 would change their provenance.
func LoadFrozen(manifestPath, profilesPath, modelLockPath string) (Frozen, error) {
	var out Frozen
	manifestPath, err := filepath.Abs(manifestPath)
	if err != nil || filepath.Base(filepath.Dir(manifestPath)) != "manifest" {
		return out, ErrArtifact
	}
	manifestRaw, err := os.ReadFile(manifestPath)
	if err != nil || len(manifestRaw) == 0 || len(manifestRaw) > 1<<20 {
		return out, ErrArtifact
	}
	manifestHash := digest(manifestRaw)
	if filepath.Base(manifestPath) != manifestHash+".json" || exactJSON(manifestRaw, &out.Manifest) != nil {
		return out, ErrArtifact
	}
	out.ManifestSHA256 = manifestHash
	m := out.Manifest
	if m.SchemaVersion != RepresentationSchema || m.Status != "candidate_default_off" ||
		m.DatasetID == "" || m.DatasetManifestSHA256 == "" ||
		m.DatasetRevision != 2 || m.SourceRowCount < 1 || m.WarehouseGeneration == "" || m.DataKind != "synthetic" ||
		m.ModelRepository == "" || m.ModelRevision == "" || m.TokenizerID == "" ||
		len(m.Lanes) != 3 || len(m.Shards) != 2 || m.Shards[0].Kind != "query" || m.Shards[1].Kind != "chunk" {
		return Frozen{}, ErrArtifact
	}
	profileRaw, err := os.ReadFile(profilesPath)
	if err != nil || digest(profileRaw) != m.ProfilesSHA256 {
		return Frozen{}, ErrArtifact
	}
	var pinned map[string]LaneProfile
	if exactJSON(profileRaw, &pinned) != nil || !reflect.DeepEqual(pinned, m.Lanes) {
		return Frozen{}, ErrArtifact
	}
	lockRaw, err := os.ReadFile(modelLockPath)
	if err != nil || digest(lockRaw) != m.ModelLockSHA256 {
		return Frozen{}, ErrArtifact
	}
	var lock struct {
		Repository string `json:"repository"`
		Revision   string `json:"revision"`
	}
	if json.Unmarshal(lockRaw, &lock) != nil || lock.Repository != m.ModelRepository || lock.Revision != m.ModelRevision {
		return Frozen{}, ErrArtifact
	}
	for lane, kind := range map[string]representation.Kind{"dense": representation.Dense, "sparse": representation.Sparse, "token_matrix": representation.TokenMatrix} {
		profile, ok := m.Lanes[lane]
		if !ok || profile.RepresentationContract.Kind != kind || profile.RepresentationContract.Validate() != nil ||
			profile.RepresentationContract.TokenizerID != m.TokenizerID ||
			profile.OutputContract != representation.OutputContract(kind) ||
			profile.RepresentationSpace == "" || profile.Model == "" ||
			profile.MaxBatch < 1 || profile.MaxBatch > representation.MaxBatch || profile.MaxInputTokens < 1 {
			return Frozen{}, ErrArtifact
		}
	}
	root := filepath.Dir(filepath.Dir(manifestPath))
	seenShard := map[string]bool{}
	for _, shard := range m.Shards {
		directory := map[string]string{"query": "queries", "chunk": "chunks"}[shard.Kind]
		if directory == "" || !strings.HasPrefix(shard.Path, "shards/"+directory+"/") ||
			filepath.IsAbs(shard.Path) || filepath.Clean(shard.Path) != shard.Path || strings.Contains(shard.Path, "..") ||
			shard.Rows < 1 || shard.SizeBytes < 1 || shard.SizeBytes > 64<<20 || seenShard[shard.Path] ||
			filepath.Base(shard.Path) != shard.SHA256+".jsonl" {
			return Frozen{}, ErrArtifact
		}
		seenShard[shard.Path] = true
		body, err := os.ReadFile(filepath.Join(root, shard.Path))
		if err != nil || int64(len(body)) != shard.SizeBytes || digest(body) != shard.SHA256 {
			return Frozen{}, ErrArtifact
		}
		switch shard.Kind {
		case "query":
			if err := appendJSONL(body, shard.Rows, &out.Queries); err != nil {
				return Frozen{}, err
			}
		case "chunk":
			if err := appendJSONL(body, shard.Rows, &out.Chunks); err != nil {
				return Frozen{}, err
			}
		}
	}
	if len(out.Queries) == 0 || len(out.Chunks) == 0 || len(out.Chunks) > 1000 ||
		len(out.Queries) != m.QueryCount || len(out.Chunks) != m.ChunkCount {
		return Frozen{}, ErrArtifact
	}
	if err := out.validateRows(); err != nil {
		return Frozen{}, err
	}
	return out, nil
}

func appendJSONL[T any](body []byte, expected int, out *[]T) error {
	if len(body) == 0 || body[len(body)-1] != '\n' {
		return ErrArtifact
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), 4<<20)
	count := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] != '{' || !bytes.Equal(line, trimmed) {
			return ErrArtifact
		}
		var row T
		if exactJSON(line, &row) != nil {
			return ErrArtifact
		}
		*out = append(*out, row)
		count++
	}
	if scanner.Err() != nil || count != expected {
		return ErrArtifact
	}
	return nil
}

func (f Frozen) validateRows() error {
	queries := map[string]bool{}
	texts := map[string]Representations{}
	for _, q := range f.Queries {
		if q.QueryID == "" || queries[q.QueryID] || q.QueryFamilyID == "" || q.NearDuplicateClusterID == "" ||
			(q.Split != "train" && q.Split != "validation" && q.Split != "test") ||
			strings.TrimSpace(q.Text) == "" || q.TextSHA256 != digest([]byte(q.Text)) ||
			f.validateRepresentations(q.Representations) != nil {
			return ErrArtifact
		}
		queries[q.QueryID] = true
		if old, ok := texts[q.Text]; ok && !reflect.DeepEqual(old, q.Representations) {
			return ErrArtifact // one model/text cannot produce two frozen values
		}
		texts[q.Text] = q.Representations
	}
	seenChunks := map[string]bool{}
	for _, c := range f.Chunks {
		key := c.DocumentID + "\x00" + c.DocumentRevision + "\x00" + c.ChunkID
		if c.DocumentID == "" || c.DocumentRevision == "" || c.ChunkID == "" || c.ChunkKey == "" ||
			seenChunks[key] || strings.TrimSpace(c.Text) == "" || c.TextSHA256 != digest([]byte(c.Text)) ||
			c.ContentAvailableAt == "" || f.validateRepresentations(c.Representations) != nil {
			return ErrArtifact
		}
		seenChunks[key] = true
		availableAt, err := time.Parse(time.RFC3339Nano, c.ContentAvailableAt)
		if err != nil {
			return ErrArtifact
		}
		_, offset := availableAt.Zone()
		if offset != 0 {
			return ErrArtifact
		}
		var tuple []string
		if json.Unmarshal([]byte(c.ChunkKey), &tuple) != nil || len(tuple) != 3 ||
			tuple[0] != c.DocumentID || tuple[1] != c.DocumentRevision || tuple[2] != c.ChunkID {
			return ErrArtifact
		}
		compactKey, _ := json.Marshal(tuple)
		if string(compactKey) != c.ChunkKey {
			return ErrArtifact
		}
	}
	return nil
}

func (f Frozen) validateRepresentations(v Representations) error {
	if v.Dense == nil || v.Sparse == nil || v.TokenMatrix == nil {
		return ErrArtifact
	}
	denseContract := f.Manifest.Lanes["dense"].RepresentationContract
	if len(v.Dense.Values) != denseContract.Dimensions {
		return ErrArtifact
	}
	norm := 0.0
	for _, x := range v.Dense.Values {
		if math.IsInf(x, 0) || math.IsNaN(x) {
			return ErrArtifact
		}
		norm += x * x
	}
	if norm <= 0 || denseContract.Normalization == "l2" && math.Abs(norm-1) > 1e-4 {
		return ErrArtifact
	}
	sparseContract := f.Manifest.Lanes["sparse"].RepresentationContract
	if len(v.Sparse.Indices) == 0 || len(v.Sparse.Indices) != len(v.Sparse.Weights) || len(v.Sparse.Indices) > sparseContract.MaxNonzero {
		return ErrArtifact
	}
	last := -1
	for i, id := range v.Sparse.Indices {
		weight := v.Sparse.Weights[i]
		if id <= last || id >= sparseContract.Dimensions || weight <= 0 || math.IsInf(weight, 0) || math.IsNaN(weight) {
			return ErrArtifact
		}
		last = id
	}
	if err := multivector.ValidateMatrix(*v.TokenMatrix, f.Manifest.Lanes["token_matrix"].RepresentationContract); err != nil {
		return fmt.Errorf("%w: token matrix shape/mask: %v", ErrArtifact, err)
	}
	for _, valid := range v.TokenMatrix.Mask {
		if !valid { // encoder v1 freezes only effective ColBERT rows
			return ErrArtifact
		}
	}
	return nil
}
