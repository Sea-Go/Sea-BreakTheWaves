package sourceproof

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const rtwVersionWitnessPath = "/internal/v1/knowledge/wiki-quality/source-version-witness/read"
const sourceVersionResponseLimit = 20 << 20

// HTTPSourceVersionReader reads an RTW-owned current V from the Worker-only
// boundary. The bearer is a service credential, never a product User JWT.
type HTTPSourceVersionReader struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewHTTPSourceVersionReader(baseURL, workerToken string) (*HTTPSourceVersionReader, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed == nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") ||
		parsed.RawQuery != "" || parsed.Fragment != "" ||
		workerToken == "" || strings.TrimSpace(workerToken) != workerToken ||
		strings.ContainsAny(workerToken, "\r\n") {
		return nil, ErrDCCutoffAlignment
	}
	return &HTTPSourceVersionReader{
		baseURL: strings.TrimRight(baseURL, "/"), token: workerToken,
		client: &http.Client{Timeout: 20 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			}}}, nil
}

func (r *HTTPSourceVersionReader) ReadWikiQualitySourceVersionCandidate(ctx context.Context,
	pinned PinnedRequest, version int64) (RTWSourceVersionCandidate, error) {
	var result RTWSourceVersionCandidate
	if r == nil || r.client == nil || ctx == nil || ctx.Err() != nil ||
		!idPattern.MatchString(pinned.ModuleID) ||
		!idPattern.MatchString(pinned.WikiRevisionID) ||
		!idPattern.MatchString(pinned.FactSetRevisionID) ||
		pinned.PageID == "" || len(pinned.PageID) > 200 ||
		strings.ContainsAny(pinned.PageID, "/?#\r\n") ||
		len(pinned.SourceScopeRevision) != len("scope_")+64 ||
		!validSHA(pinned.SourceScopeRevision[len("scope_"):]) ||
		version < 1 || version > sourceVersionLimit {
		return result, ErrDCCutoffAlignment
	}
	requestRaw, err := json.Marshal(struct {
		ModuleID            string `json:"module_id"`
		PageID              string `json:"page_id"`
		FactSetRevisionID   string `json:"fact_set_revision_id"`
		WikiRevisionID      string `json:"wiki_revision_id"`
		SourceScopeRevision string `json:"source_scope_revision"`
		SourceVersion       int64  `json:"source_version"`
	}{pinned.ModuleID, pinned.PageID, pinned.FactSetRevisionID,
		pinned.WikiRevisionID, pinned.SourceScopeRevision, version})
	if err != nil {
		return result, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		r.baseURL+rtwVersionWitnessPath, bytes.NewReader(requestRaw))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.token)
	resp, err := r.client.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK ||
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		return result, ErrDCCutoffAlignment
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, sourceVersionResponseLimit+1))
	if err != nil || len(body) < 2 || len(body) > sourceVersionResponseLimit ||
		rejectDuplicateJSONKeys(body) != nil {
		return result, ErrDCCutoffAlignment
	}
	var envelope struct {
		Code int                       `json:"code"`
		Msg  string                    `json:"msg"`
		Data RTWSourceVersionCandidate `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF ||
		envelope.Code != http.StatusOK ||
		envelope.Data.SchemaVersion != sourceVersionSchema ||
		envelope.Data.SourceVersion != version ||
		envelope.Data.Catalog.FactSetRevisionID != pinned.FactSetRevisionID ||
		envelope.Data.Catalog.WikiRevisionID != pinned.WikiRevisionID ||
		envelope.Data.Catalog.SourceScopeRevision != pinned.SourceScopeRevision {
		return result, ErrDCCutoffAlignment
	}
	return envelope.Data, nil
}

// JSON Decoder's strict-field mode ignores duplicate object keys. Reject
// those bytes before a later response value can override a prior identity.
func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var visit func() error
	visit = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, compound := token.(json.Delim)
		if !compound {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				key, ok := keyToken.(string)
				if err != nil || !ok || seen[key] {
					return ErrDCCutoffAlignment
				}
				seen[key] = true
				if err := visit(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return ErrDCCutoffAlignment
			}
		case '[':
			for decoder.More() {
				if err := visit(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return ErrDCCutoffAlignment
			}
		default:
			return ErrDCCutoffAlignment
		}
		return nil
	}
	if err := visit(); err != nil {
		return err
	}
	if err := visit(); !errors.Is(err, io.EOF) {
		return ErrDCCutoffAlignment
	}
	return nil
}
