// Package ridethewind consumes the authority's worker API, never its storage.
package ridethewind

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
)

type Client struct{ http *httpclient.Client }

func New(config httpclient.Config) (*Client, error) {
	c, err := httpclient.New(config)
	if err != nil {
		return nil, err
	}
	return &Client{c}, nil
}

func request[T any](ctx context.Context, c *Client, method, resource, id, action string, input any) (T, error) {
	var zero T
	segment, err := httpclient.Segment(id)
	if err != nil {
		return zero, err
	}
	raw, _, err := c.http.Do(ctx, method, "/internal/v1/knowledge/"+resource+"/"+segment+action, nil, input, "")
	if err != nil {
		return zero, err
	}
	return decodeEnvelope[T](raw)
}

func decodeEnvelope[T any](raw []byte) (T, error) {
	var zero T
	var response struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data *T     `json:"data"`
	}
	if err := httpclient.Decode(raw, &response); err != nil {
		return zero, err
	}
	if response.Code != 200 {
		return zero, fmt.Errorf("RTW response code %d: %s", response.Code, response.Msg)
	}
	if response.Data == nil {
		return zero, errors.New("RTW response missing data")
	}
	return *response.Data, nil
}

func post[T any](ctx context.Context, c *Client, path, idempotency string, input any) (T, error) {
	var zero T
	raw, _, err := c.http.Do(ctx, http.MethodPost, "/internal/v1/knowledge/"+path, nil, input, idempotency)
	if err != nil {
		return zero, err
	}
	return decodeEnvelope[T](raw)
}
func (c *Client) GetRevision(ctx context.Context, id string) (Revision, error) {
	v, e := request[Revision](ctx, c, http.MethodGet, "revisions", id, "", nil)
	if e == nil && v.RevisionId != id {
		e = errors.New("RTW revision identity mismatch")
	}
	return v, e
}
func (c *Client) GetRelease(ctx context.Context, id string) (Release, error) {
	v, e := request[Release](ctx, c, http.MethodGet, "releases", id, "", nil)
	if e == nil && v.ReleaseId != id {
		e = errors.New("RTW release identity mismatch")
	}
	return v, e
}
func (c *Client) GetBuild(ctx context.Context, id string) (Build, error) {
	v, e := request[Build](ctx, c, http.MethodGet, "builds", id, "", nil)
	if e == nil && v.BuildId != id {
		e = errors.New("RTW build identity mismatch")
	}
	return v, e
}
func (c *Client) GetCompile(ctx context.Context, id string) (Compile, error) {
	v, e := request[Compile](ctx, c, http.MethodGet, "compiles", id, "", nil)
	if e == nil && v.CompileId != id {
		e = errors.New("RTW compile identity mismatch")
	}
	return v, e
}

func (c *Client) ReadSearchSource(ctx context.Context, q ReadSearchSourceReq) (CitationChunk, error) {
	chunk, err := post[CitationChunk](ctx, c, "search-sources/read", "", q)
	if err != nil {
		return CitationChunk{}, err
	}
	if chunk.ChunkId != q.ChunkId || chunk.RevisionId != q.RevisionId || chunk.ContentId == "" ||
		chunk.SourceKind == "" || chunk.Original.Key == "" || chunk.Original.Sha256 == "" ||
		chunk.Location.Locator == "" || chunk.Text == "" || chunk.TextHash == "" {
		return CitationChunk{}, errors.New("RTW search source identity or body mismatch")
	}
	return chunk, nil
}

func (c *Client) AcceptSearchCitations(ctx context.Context, q AcceptSearchCitationsReq) (SearchCitationReceipt, error) {
	if q.SearchId == "" || q.PackHash == "" || q.PackJson == "" {
		return SearchCitationReceipt{}, errors.New("search citation request requires fixed identity and pack")
	}
	receipt, err := post[SearchCitationReceipt](ctx, c, "search-citations", q.SearchId, q)
	if err != nil {
		return SearchCitationReceipt{}, err
	}
	if receipt.SearchId != q.SearchId || receipt.PackHash != q.PackHash || receipt.DurableRef == "" {
		return SearchCitationReceipt{}, errors.New("RTW citation receipt mismatch")
	}
	return receipt, nil
}

func (c *Client) GetSearchCitations(ctx context.Context, searchID string) (SearchCitationRecord, error) {
	record, err := request[SearchCitationRecord](ctx, c, http.MethodGet, "search-citations", searchID, "", nil)
	if err != nil {
		return SearchCitationRecord{}, err
	}
	if record.SearchId != searchID || record.PackHash == "" || record.DurableRef == "" {
		return SearchCitationRecord{}, errors.New("RTW citation lookup identity mismatch")
	}
	return record, nil
}
func (c *Client) ClaimBuild(ctx context.Context, q ClaimBuildReq) (Build, error) {
	v, e := request[Build](ctx, c, http.MethodPost, "builds", q.BuildId, "/claim", q)
	if e == nil && (v.BuildId != q.BuildId || v.Generation != q.Generation || v.AttemptId != q.AttemptId || v.LeaseEpoch != q.LeaseEpoch || v.CancelVersion != q.CancelVersion || v.ManifestHash != q.ManifestHash) {
		e = errors.New("RTW build fence mismatch")
	}
	if e == nil && (v.State != "BUILDING" || v.LeaseExpiresAt != q.LeaseExpiresAt) {
		e = errors.New("RTW ClaimBuild result mismatch")
	}
	return v, e
}
func (c *Client) AcceptBuild(ctx context.Context, q AcceptBuildReq) (Build, error) {
	v, e := request[Build](ctx, c, http.MethodPost, "builds", q.BuildId, "/results", q)
	if e == nil && (v.BuildId != q.BuildId || v.Generation != q.Generation || v.AttemptId != q.AttemptId || v.LeaseEpoch != q.LeaseEpoch || v.CancelVersion != q.CancelVersion || v.ManifestHash != q.ManifestHash) {
		e = errors.New("RTW build fence mismatch")
	}
	if e == nil && (v.State != q.State || v.IndexManifestRef != q.IndexManifestRef || v.IndexManifestHash != q.IndexManifestHash || v.ErrorCode != q.ErrorCode) {
		e = errors.New("RTW AcceptBuild result mismatch")
	}
	return v, e
}
func (c *Client) ClaimCompile(ctx context.Context, q ClaimCompileReq) (Compile, error) {
	v, e := request[Compile](ctx, c, http.MethodPost, "compiles", q.CompileId, "/claim", q)
	if e == nil && (v.CompileId != q.CompileId || v.Generation != q.Generation || v.AttemptId != q.AttemptId || v.LeaseEpoch != q.LeaseEpoch || v.CancelVersion != q.CancelVersion || v.InputHash != q.InputHash) {
		e = errors.New("RTW compile fence mismatch")
	}
	if e == nil && (v.State != "BUILDING" || v.LeaseExpiresAt != q.LeaseExpiresAt) {
		e = errors.New("RTW ClaimCompile result mismatch")
	}
	return v, e
}
func (c *Client) AcceptCompile(ctx context.Context, q AcceptCompileReq) (Compile, error) {
	expectedState := "ACCEPTED"
	if q.State == "FAILED" {
		expectedState = "FAILED"
	}
	v, e := request[Compile](ctx, c, http.MethodPost, "compiles", q.CompileId, "/results", q)
	if e == nil && (v.CompileId != q.CompileId || v.Generation != q.Generation || v.AttemptId != q.AttemptId || v.LeaseEpoch != q.LeaseEpoch || v.CancelVersion != q.CancelVersion || v.InputHash != q.InputHash) {
		e = errors.New("RTW compile fence mismatch")
	}
	if e == nil && (v.State != expectedState || v.ErrorCode != q.ErrorCode || v.ResultHash == "" || (expectedState == "ACCEPTED" && v.RevisionId == "")) {
		e = errors.New("RTW AcceptCompile result mismatch")
	}
	return v, e
}
