package inference

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDataCenterManifestSelectsRepresentationWithoutLocalModelConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/model/manifest" || r.Header.Get("Authorization") != "Bearer workload-token" {
			t.Errorf("wrong DataCenter request: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"retrieval":{"model":"managed-encoder","space":"managed-space","dimensions":1024,"dense":"dense_current","sparse":"sparse_current","multi":"multi_current"},"chat":{"agent":"agent","planning":"planning","content_identity":"physical-content-1"}}`))
	}))
	defer server.Close()
	client := New(server.URL+"/v1/model", "workload-token", 2*time.Second)
	manifest, err := client.Manifest(context.Background())
	if err != nil || manifest.Retrieval.Model != "managed-encoder" || manifest.Retrieval.Space != "managed-space" || manifest.Chat.Planning != "planning" || manifest.Chat.ContentIdentity != "physical-content-1" {
		t.Fatalf("DataCenter contract was lost: %+v %v", manifest, err)
	}
}
