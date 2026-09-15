package wiki_quality

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// OriginalParagraphBytes follows RTW's immutable-revision paragraph:N rule:
// CRLF is normalized only to locate blank-line blocks, empty blocks are
// skipped, and the returned byte offsets still address the ORIGINAL object.
// A four-space Markdown code block is never TrimSpace'd into prose.
func OriginalParagraphBytes(content, locator string) (startByte, endByte int, ok bool) {
	parts := paragraphPattern.FindStringSubmatch(locator)
	if len(parts) != 2 || !utf8.ValidString(content) {
		return 0, 0, false
	}
	wanted, err := strconv.Atoi(parts[1])
	if err != nil || wanted < 1 {
		return 0, 0, false
	}
	var normalized strings.Builder
	positions := make([]int, 0, len(content)+1)
	for i := 0; i < len(content); i++ {
		positions = append(positions, i)
		if content[i] == '\r' && i+1 < len(content) && content[i+1] == '\n' {
			i++
			normalized.WriteByte('\n')
		} else {
			normalized.WriteByte(content[i])
		}
	}
	positions = append(positions, len(content))
	start, current := 0, 0
	for _, block := range strings.Split(normalized.String(), "\n\n") {
		end := start + len(block)
		if strings.TrimSpace(block) != "" {
			current++
			if current == wanted {
				return positions[start], positions[end], true
			}
		}
		start = end + 2
	}
	return 0, 0, false
}

func sourceQuote(fact Fact, source Source) bool {
	start, end, ok := OriginalParagraphBytes(source.Content, fact.Locator)
	if !ok || fact.OriginalByteStart < start || fact.OriginalByteEnd > end ||
		fact.OriginalByteEnd <= fact.OriginalByteStart ||
		!utf8.ValidString(fact.Quote) || strings.TrimSpace(fact.Quote) == "" ||
		fact.OriginalByteSHA256 != Digest([]byte(fact.Quote)) {
		return false
	}
	raw := []byte(source.Content)
	return string(raw[fact.OriginalByteStart:fact.OriginalByteEnd]) == fact.Quote &&
		utf8.Valid(raw[:fact.OriginalByteStart]) && utf8.Valid(raw[:fact.OriginalByteEnd])
}
