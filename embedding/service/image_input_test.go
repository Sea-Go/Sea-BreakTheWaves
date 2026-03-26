package service

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeImageInputConvertsLocalURLToDataURI(t *testing.T) {
	imageBytes := []byte{0x89, 0x50, 0x4e, 0x47}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageBytes)
	}))
	defer srv.Close()

	got, err := normalizeImageInput(srv.URL + "/test.png")
	if err != nil {
		t.Fatalf("normalizeImageInput returned error: %v", err)
	}
	wantPrefix := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes)
	if got != wantPrefix {
		t.Fatalf("unexpected normalized image input: %s", got)
	}
}

func TestNormalizeImageInputKeepsPublicURL(t *testing.T) {
	raw := "https://example.com/image.png"
	got, err := normalizeImageInput(raw)
	if err != nil {
		t.Fatalf("normalizeImageInput returned error: %v", err)
	}
	if got != raw {
		t.Fatalf("expected public URL to stay unchanged, got %s", got)
	}
}

func TestNormalizeImageInputsSkipsBlankValues(t *testing.T) {
	got, err := normalizeImageInputs([]string{"", "  ", "https://example.com/a.png"})
	if err != nil {
		t.Fatalf("normalizeImageInputs returned error: %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0], "example.com") {
		t.Fatalf("unexpected normalizeImageInputs result: %#v", got)
	}
}
