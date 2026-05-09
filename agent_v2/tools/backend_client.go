package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"agent_v2/config"
)

const articleAPISuccessCode = 200

type BackendClient struct {
	articleBaseURL string
	commentBaseURL string
	authToken      string
	httpClient     *http.Client
}

type BackendClientConfig struct {
	ArticleBaseURL string
	CommentBaseURL string
	AuthToken      string
	Timeout        time.Duration
	HTTPClient     *http.Client
}

type BackendArticleDraft struct {
	Title         string   `json:"title"`
	Brief         string   `json:"brief,omitempty"`
	Content       string   `json:"content"`
	CoverImageURL string   `json:"cover_image_url,omitempty"`
	ManualTypeTag string   `json:"manual_type_tag,omitempty"`
	SecondaryTags []string `json:"secondary_tags,omitempty"`
}

type BackendCreateArticleResponse struct {
	ArticleID string `json:"article_id"`
}

type BackendCommentItem struct {
	ID       int64                `json:"id"`
	UserID   int64                `json:"user_id"`
	Content  string               `json:"content"`
	RootID   int64                `json:"root_id"`
	ParentID int64                `json:"parent_id"`
	Children []BackendCommentItem `json:"children"`
}

type backendGetCommentResponse struct {
	Comment []BackendCommentItem `json:"comment"`
}

type backendArticleResponseEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func NewBackendClientFromConfig(cfg config.BackendConfig) *BackendClient {
	cfg = cfg.WithDefaults()
	return NewBackendClient(BackendClientConfig{
		ArticleBaseURL: cfg.ArticleBaseURL,
		CommentBaseURL: cfg.CommentBaseURL,
		AuthToken:      cfg.AuthToken,
		Timeout:        time.Duration(cfg.TimeoutSeconds) * time.Second,
	})
}

func NewBackendClient(cfg BackendClientConfig) *BackendClient {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	return &BackendClient{
		articleBaseURL: strings.TrimRight(strings.TrimSpace(cfg.ArticleBaseURL), "/"),
		commentBaseURL: strings.TrimRight(strings.TrimSpace(cfg.CommentBaseURL), "/"),
		authToken:      strings.TrimSpace(cfg.AuthToken),
		httpClient:     httpClient,
	}
}

func (c *BackendClient) CreateArticle(ctx context.Context, draft BackendArticleDraft) (BackendCreateArticleResponse, error) {
	if c == nil {
		return BackendCreateArticleResponse{}, errors.New("backend client is nil")
	}
	if strings.TrimSpace(c.articleBaseURL) == "" {
		return BackendCreateArticleResponse{}, errors.New("backend article_base_url 不能为空")
	}
	if strings.TrimSpace(draft.Title) == "" {
		return BackendCreateArticleResponse{}, errors.New("article title 不能为空")
	}
	if strings.TrimSpace(draft.Content) == "" {
		return BackendCreateArticleResponse{}, errors.New("article content 不能为空")
	}

	var out BackendCreateArticleResponse
	if err := c.postJSON(ctx, c.articleBaseURL+"/v1/article", draft, &out, true); err != nil {
		return BackendCreateArticleResponse{}, err
	}
	return out, nil
}

func (c *BackendClient) ListArticleComments(ctx context.Context, articleID string, page, pageSize int64) ([]string, error) {
	if c == nil {
		return nil, errors.New("backend client is nil")
	}
	if strings.TrimSpace(c.commentBaseURL) == "" {
		return nil, errors.New("backend comment_base_url 不能为空")
	}
	articleID = strings.TrimSpace(articleID)
	if articleID == "" {
		return nil, errors.New("articleID 不能为空")
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}

	req := map[string]any{
		"target_type": "article",
		"target_id":   articleID,
		"sort_type":   1,
		"root_id":     0,
		"page":        page,
		"page_size":   pageSize,
	}
	var out backendGetCommentResponse
	if err := c.postJSON(ctx, c.commentBaseURL+"/comment/v1/comment/list", req, &out, false); err != nil {
		return nil, err
	}

	comments := make([]string, 0, len(out.Comment))
	for _, item := range out.Comment {
		collectBackendCommentContent(&comments, item)
	}
	return compactStringList(comments), nil
}

func (c *BackendClient) postJSON(ctx context.Context, endpoint string, payload any, out any, unwrapArticleEnvelope bool) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", bearerToken(c.authToken))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("backend %s returned HTTP %d: %s", endpointPath(endpoint), resp.StatusCode, strings.TrimSpace(string(data)))
	}

	if unwrapArticleEnvelope {
		return decodeArticleEnvelope(data, out)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode backend %s response failed: %w; raw=%s", endpointPath(endpoint), err, strings.TrimSpace(string(data)))
	}
	return nil
}

func decodeArticleEnvelope(data []byte, out any) error {
	var envelope backendArticleResponseEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("decode article response envelope failed: %w; raw=%s", err, strings.TrimSpace(string(data)))
	}
	if envelope.Code != articleAPISuccessCode {
		return fmt.Errorf("article api returned code=%d msg=%s", envelope.Code, envelope.Msg)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("decode article response data failed: %w; raw=%s", err, strings.TrimSpace(string(envelope.Data)))
	}
	return nil
}

func collectBackendCommentContent(out *[]string, item BackendCommentItem) {
	if text := strings.TrimSpace(item.Content); text != "" {
		*out = append(*out, text)
	}
	for _, child := range item.Children {
		collectBackendCommentContent(out, child)
	}
}

func compactStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func bearerToken(token string) string {
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		return token
	}
	return "Bearer " + token
}

func endpointPath(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	if u.Path == "" {
		return endpoint
	}
	return u.Path
}
