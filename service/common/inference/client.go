// Package inference is the generic HTTP client for configured representations and rankers.
package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/ranking"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

type Sparse struct {
	Indices []uint32  `json:"indices"`
	Values  []float32 `json:"values"`
}
type Segment struct {
	Start  int64       `json:"start"`
	End    int64       `json:"end"`
	Dense  []float32   `json:"dense"`
	Sparse Sparse      `json:"sparse"`
	Multi  [][]float32 `json:"multi"`
}
type Encoding struct {
	Model    string    `json:"model"`
	Space    string    `json:"space"`
	Segments []Segment `json:"segments"`
}
type Encoder interface {
	Encode(context.Context, string, string, []string) ([]Encoding, error)
}
type SelectiveEncoder interface {
	EncodeModes(context.Context, string, string, []string, bool, bool, bool) ([]Encoding, error)
}
type Ranker interface {
	Rank(context.Context, string, [][]float64) ([]float64, error)
}
type Client struct {
	URL              string
	Token            string
	HTTP             *http.Client
	MaxResponseBytes int64
}

type Manifest struct {
	Retrieval item.Retrieval `json:"retrieval"`
	Chat      struct {
		Agent           string `json:"agent"`
		Planning        string `json:"planning"`
		ContentIdentity string `json:"content_identity"`
	} `json:"chat"`
}

func New(url, token string, timeout time.Duration) *Client {
	return &Client{URL: strings.TrimRight(url, "/"), Token: token, HTTP: &http.Client{Timeout: timeout}, MaxResponseBytes: 128 << 20}
}

// Manifest reads the DataCenter-owned model and index contract once during
// process assembly. BTW does not select a physical model or representation.
func (c *Client) Manifest(ctx context.Context) (Manifest, error) {
	var out Manifest
	if c == nil || c.URL == "" {
		return out, item.ErrUnavailable
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/manifest", nil)
	if err != nil {
		return out, err
	}
	if c.Token != "" {
		r.Header.Set("Authorization", "Bearer "+c.Token)
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(r)
	if err != nil {
		return out, fmt.Errorf("DataCenter manifest: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return out, fmt.Errorf("DataCenter manifest returned %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&out); err != nil {
		return Manifest{}, err
	}
	rt := out.Retrieval
	if rt.Model == "" || rt.Space == "" || rt.Dimensions < 1 || rt.Dense == "" || rt.Sparse == "" || rt.Multi == "" || out.Chat.Agent == "" || out.Chat.Planning == "" || out.Chat.ContentIdentity == "" {
		return Manifest{}, item.ErrInvalid
	}
	return out, nil
}
func (c *Client) call(ctx context.Context, path string, in, out any) error {
	if c == nil || c.URL == "" {
		return item.ErrUnavailable
	}
	data, e := json.Marshal(in)
	if e != nil {
		return e
	}
	r, e := http.NewRequestWithContext(ctx, "POST", c.URL+path, bytes.NewReader(data))
	if e != nil {
		return e
	}
	r.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		r.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, e := c.HTTP.Do(r)
	if e != nil {
		return fmt.Errorf("inference: %w", e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("inference status %d: %s", resp.StatusCode, msg)
	}
	limit := c.MaxResponseBytes
	if limit < 1 {
		limit = 128 << 20
	}
	d := json.NewDecoder(io.LimitReader(resp.Body, limit))
	return d.Decode(out)
}
func (c *Client) Encode(ctx context.Context, model, space string, texts []string) ([]Encoding, error) {
	return c.EncodeModes(ctx, model, space, texts, true, true, true)
}
func (c *Client) EncodeModes(ctx context.Context, model, space string, texts []string, dense, sparse, multi bool) ([]Encoding, error) {
	if !dense && !sparse && !multi {
		return nil, item.ErrInvalid
	}
	var out struct {
		Data []Encoding `json:"data"`
	}
	e := c.call(ctx, "/encode", map[string]any{"model": model, "space": space, "texts": texts, "dense": dense, "sparse": sparse, "multi": multi}, &out)
	if e != nil {
		return nil, e
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("%w: encoding count", item.ErrInvalid)
	}
	for i, v := range out.Data {
		if v.Model != model || v.Space != space || len(v.Segments) == 0 {
			return nil, fmt.Errorf("%w: representation space", item.ErrInvalid)
		}
		end := int64(0)
		n := int64(len([]rune(texts[i])))
		for _, seg := range v.Segments {
			if seg.Start != end || seg.End <= seg.Start || seg.End > n || (dense && len(seg.Dense) == 0) || len(seg.Sparse.Indices) != len(seg.Sparse.Values) || (multi && len(seg.Multi) == 0) {
				return nil, fmt.Errorf("%w: incomplete encoding coverage", item.ErrInvalid)
			}
			for _, f := range seg.Dense {
				if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
					return nil, item.ErrInvalid
				}
			}
			end = seg.End
		}
		if end != n {
			return nil, fmt.Errorf("%w: truncated representation", item.ErrInvalid)
		}
	}
	return out.Data, nil
}
func (c *Client) Rank(ctx context.Context, model string, features [][]float64) ([]float64, error) {
	var out struct {
		Model  string    `json:"model"`
		Scores []float64 `json:"scores"`
	}
	e := c.call(ctx, "/rank", map[string]any{"model": model, "features": features}, &out)
	if e != nil {
		return nil, e
	}
	if out.Model != model || len(out.Scores) != len(features) {
		return nil, item.ErrInvalid
	}
	for _, f := range out.Scores {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, item.ErrInvalid
		}
	}
	return out.Scores, nil
}
func (c *Client) Ready(ctx context.Context, r item.Retrieval) error {
	var out struct {
		Ready bool `json:"ready"`
	}
	if e := c.call(ctx, "/ready", map[string]any{"model": r.Model, "space": r.Space, "dimensions": r.Dimensions, "search_ranker": r.SearchRanker, "recommend_ranker": r.RecommendRanker, "search_features": ranking.SearchFeatureNames, "recommend_features": ranking.FeatureNames, "item_tower": r.ItemTower, "user_space": r.UserSpace, "match_dimensions": r.MatchDimensions, "match": r.Match}, &out); e != nil {
		return e
	}
	if !out.Ready {
		return item.ErrUnavailable
	}
	return nil
}

type Projector interface {
	Project(context.Context, string, string, string, string, [][]float32) ([][]float32, error)
}

func (c *Client) Project(ctx context.Context, model, space, inputModel, inputSpace string, vectors [][]float32) ([][]float32, error) {
	var out struct {
		Model   string      `json:"model"`
		Space   string      `json:"space"`
		Vectors [][]float32 `json:"vectors"`
	}
	if e := c.call(ctx, "/item-project", map[string]any{"model": model, "space": space, "input_model": inputModel, "input_space": inputSpace, "vectors": vectors}, &out); e != nil {
		return nil, e
	}
	if out.Model != model || out.Space != space || len(out.Vectors) != len(vectors) {
		return nil, item.ErrInvalid
	}
	return out.Vectors, nil
}
