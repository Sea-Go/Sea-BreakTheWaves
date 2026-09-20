package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/model"
)

// This optional test uses one fresh local DC Gateway/native app_user session
// furnished by the cross-repository acceptance owner. It never manufactures
// a configured model candidate or logs the protected bearer. The Wiki worker
// and real RTW source are separate owners and are not exercised here.
func TestWikiCallPointTrueDCGatewayCandidate(t *testing.T) {
	path := os.Getenv("SEA_WIKI_CALLPOINT_DC_RUNTIME_FILE")
	if path == "" {
		t.Skip("explicit 0600 local DC consumer-runtime required")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("Wiki DC runtime must be a private regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 || len(raw) > 4096 {
		t.Fatal("Wiki DC runtime unreadable or unbounded")
	}
	var runtime struct {
		BaseURL      string `json:"base_url"`
		NativeBearer string `json:"native_bearer"`
		RunID        string `json:"run_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&runtime) != nil || decoder.Decode(new(any)) != io.EOF {
		t.Fatal("Wiki DC runtime has an unexpected shape")
	}
	parsed, err := url.Parse(runtime.BaseURL)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" ||
		parsed.Fragment != "" || parsed.User != nil ||
		!wikiCompileRefID.MatchString(runtime.RunID) ||
		len("wiki-runtime-"+runtime.RunID) > 128 {
		t.Fatal("Wiki DC runtime is not a new local gateway run")
	}
	const source = "The verified external knowledge source contains a concise fact about Wiki maintenance."
	const prompt = "Draft a short Wiki entry grounded only in the verified external source fact."
	sourceHash := sha256.Sum256([]byte(source))
	sourceRefsSHA, err := WikiCompileSourceRefsSHA([]WikiCompileSourceIdentity{
		{RevisionID: "rtw-source-runtime-probe", ContentHash: hex.EncodeToString(sourceHash[:])},
	})
	if err != nil {
		t.Fatal(err)
	}
	inputHash := sha256.Sum256([]byte(prompt + "\n" + source))
	ref := WikiCompileModelInvocationRef{
		CompileID:  "wiki-runtime-" + runtime.RunID,
		InputHash:  hex.EncodeToString(inputHash[:]),
		Generation: 1, SourceRefsSHA: sourceRefsSHA,
		ModelStage: WikiCompileModelStageWikiDraft,
	}
	m, err := NewWikiCompileCallPointModel(WikiCompileCallPointModelConfig{
		BaseURL: runtime.BaseURL, NativeBearer: runtime.NativeBearer,
		CallPoint: WikiCompileCallPoint,
	}, ref)
	if err != nil {
		t.Fatal("fresh native Wiki CallPoint model could not be assembled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	q := &model.Request{Messages: []model.Message{
		model.NewSystemMessage("You are a Wiki compiler. Use only the supplied fixed source."),
		model.NewUserMessage(prompt + "\nSource: " + source),
	}, GenerationConfig: model.GenerationConfig{MaxTokens: model.IntPtr(1024), Stream: false}}
	ch, err := m.GenerateContent(ctx, q)
	if err != nil || ch == nil {
		t.Fatal("official Wiki model invocation could not start")
	}
	final := 0
	for response := range ch {
		if response == nil {
			continue
		}
		if response.Error != nil {
			t.Fatal("actual DC CallPoint model returned a terminal error")
		}
		if response.Done && !response.IsPartial {
			if len(response.Choices) != 1 || response.Choices[0].FinishReason == nil ||
				strings.TrimSpace(*response.Choices[0].FinishReason) == "" {
				t.Fatal("actual DC model omitted a complete finish_reason")
			}
			final++
		}
	}
	if err := ctx.Err(); err != nil || final != 1 {
		t.Fatalf("actual DC model did not complete one call: terminal=%d err=%v", final, err)
	}
}
