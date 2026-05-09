package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBackendClientCreateArticle(t *testing.T) {
	var gotAuth string
	var gotReq BackendArticleDraft
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/article" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"msg":  "OK",
			"data": map[string]string{
				"article_id": "article-1",
			},
		})
	}))
	defer server.Close()

	client := NewBackendClient(BackendClientConfig{
		ArticleBaseURL: server.URL,
		CommentBaseURL: server.URL,
		AuthToken:      "token",
		Timeout:        time.Second,
	})
	got, err := client.CreateArticle(context.Background(), BackendArticleDraft{
		Title:         "成都三天慢旅行",
		Brief:         "摘要",
		Content:       "正文",
		ManualTypeTag: "旅游攻略",
		SecondaryTags: []string{"成都"},
	})
	if err != nil {
		t.Fatalf("CreateArticle() error = %v", err)
	}
	if got.ArticleID != "article-1" {
		t.Fatalf("article id = %q", got.ArticleID)
	}
	if gotAuth != "Bearer token" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotReq.Title != "成都三天慢旅行" || gotReq.ManualTypeTag != "旅游攻略" {
		t.Fatalf("request = %#v", gotReq)
	}
}

func TestBackendClientListArticleComments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/comment/v1/comment/list" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"comment": []map[string]any{
				{
					"id":      1,
					"content": "交通说明可以再细一点",
					"children": []map[string]any{
						{"id": 2, "content": "预算也想看"},
					},
				},
				{
					"id":      3,
					"content": "交通说明可以再细一点",
					"children": []map[string]any{
						{"id": 4, "content": " "},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := NewBackendClient(BackendClientConfig{
		ArticleBaseURL: server.URL,
		CommentBaseURL: server.URL,
		Timeout:        time.Second,
	})
	got, err := client.ListArticleComments(context.Background(), "article-1", 1, 20)
	if err != nil {
		t.Fatalf("ListArticleComments() error = %v", err)
	}
	want := []string{"交通说明可以再细一点", "预算也想看", "交通说明可以再细一点"}
	if len(got) != len(want) {
		t.Fatalf("comments = %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("comments = %#v", got)
		}
	}
}

func TestBackendClientCreateArticleRejectsBusinessError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 1005,
			"msg":  "请先登录",
			"data": nil,
		})
	}))
	defer server.Close()

	client := NewBackendClient(BackendClientConfig{
		ArticleBaseURL: server.URL,
		CommentBaseURL: server.URL,
		Timeout:        time.Second,
	})
	_, err := client.CreateArticle(context.Background(), BackendArticleDraft{
		Title:   "标题",
		Content: "正文",
	})
	if err == nil {
		t.Fatalf("expected business error")
	}
}
