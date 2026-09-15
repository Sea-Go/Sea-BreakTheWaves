package wiki_quality

import (
	"bytes"
	"testing"
)

func TestOriginalParagraphBytesKeepsCRLFFourSpaceAndExactQuote(t *testing.T) {
	content := "\r\n\r\n海风来自东侧。\r\n    code: 7\r\n\r\n潮汐每日两次。"
	firstStart, firstEnd, ok := OriginalParagraphBytes(content, "paragraph:1")
	if !ok || string([]byte(content)[firstStart:firstEnd]) !=
		"海风来自东侧。\r\n    code: 7" {
		t.Fatal("RTW immutable paragraph offsets stripped raw CRLF/code bytes")
	}
	secondStart, secondEnd, ok := OriginalParagraphBytes(content, "paragraph:2")
	if !ok || string([]byte(content)[secondStart:secondEnd]) != "潮汐每日两次。" ||
		secondStart <= firstEnd {
		t.Fatal("blank blocks shifted later paragraph ordinal or raw offsets")
	}
	quote := "\r\n    code: 7"
	position := bytes.Index([]byte(content)[firstStart:firstEnd], []byte(quote))
	if position < 0 {
		t.Fatal("original quote not in first raw paragraph")
	}
	fact := Fact{FactID: "code-fact", SourceRevisionID: "source-r1",
		Locator: "paragraph:1", OriginalByteStart: firstStart + position,
		OriginalByteEnd: firstStart + position + len(quote),
		Quote:           quote, OriginalByteSHA256: Digest([]byte(quote))}
	source := Source{RevisionID: "source-r1", Content: content,
		ContentSHA256: Digest([]byte(content))}
	if !sourceQuote(fact, source) {
		t.Fatal("exact CRLF and four-space source quote lost original byte identity")
	}
	fact.OriginalByteStart++
	if sourceQuote(fact, source) {
		t.Fatal("one-byte-shifted source quote passed raw-object validation")
	}
	for _, locator := range []string{"paragraph:0", "paragraph:03", "paragraph:3", "paragraph:1 ", "Paragraph:1"} {
		if _, _, ok := OriginalParagraphBytes(content, locator); ok {
			t.Fatalf("malformed or absent RTW locator passed: %q", locator)
		}
	}
}
