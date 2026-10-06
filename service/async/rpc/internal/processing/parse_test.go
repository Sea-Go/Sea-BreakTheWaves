package processing

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/blob"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"io"
	"strings"
	"testing"
)

func TestLongUnicodeLineRetainsEveryCharacter(t *testing.T) {
	store, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	text := "# 标题\n" + strings.Repeat("鞘翅目🌊", 30000) + "\n## 结尾\n稀有词。"
	ctx := context.Background()
	if _, err = store.Put(ctx, "original", strings.NewReader(text), int64(len(text)), "text/markdown"); err != nil {
		t.Fatal(err)
	}
	parsed, err := (Parser{Objects: store, MaxUncompressed: 2 << 20, Attempt: "test"}).Parse(ctx, item.Revision{ID: "revision", FileKey: "original", MediaType: "text/markdown", Title: "book"})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := store.Open(ctx, parsed.TextKey, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	actual, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != text {
		t.Fatalf("text changed: got %d bytes want %d", len(actual), len(text))
	}
	if len(parsed.Sections) != 2 {
		t.Fatalf("natural headings: %d", len(parsed.Sections))
	}
}
