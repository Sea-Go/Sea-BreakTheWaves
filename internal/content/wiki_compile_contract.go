package content

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
)

const maxWikiCompileSourceBytes = 64 << 10
const maxWikiCompileOutputBytes = 64 << 10

var ErrWikiCompileContract = errors.New("wiki compile candidate differs from fixed source and attempt")

var wikiCompileID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
var wikiCompileSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)
var wikiParagraph = regexp.MustCompile(`^paragraph:([1-9][0-9]*)$`)

func validWikiPageID(value string) bool {
	// RTW page_id is a product entity_id, not a generated Compile/Revision ID.
	// It may contain a human-readable Unicode name and stays out of log labels.
	return strings.TrimSpace(value) != "" && len(value) <= 128 &&
		utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

// WikiCompileSource is one RTW-owned immutable source revision. The caller
// reads it by the frozen SourceRevisionID before starting a model request.
// This bounded local candidate does not claim to handle a complete book.
type WikiCompileSource struct {
	RevisionID string `json:"revision_id"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	SHA256     string `json:"content_hash"`
}

// WikiCompileInput is a fixed RTW compile ticket plus one DC-owned execution
// attempt. An Agent can propose prose and citations, never change this scope.
type WikiCompileInput struct {
	CompileID     string              `json:"compile_id"`
	ModuleID      string              `json:"module_id"`
	PageID        string              `json:"page_id"`
	BaseRevision  string              `json:"base_revision_id"`
	Generation    int64               `json:"generation"`
	InputHash     string              `json:"input_hash"`
	AttemptID     string              `json:"attempt_id"`
	LeaseEpoch    int64               `json:"lease_epoch"`
	CancelVersion int64               `json:"cancel_version"`
	Guidance      string              `json:"guidance"`
	SourceIDs     []string            `json:"source_revision_ids"`
	Sources       []WikiCompileSource `json:"sources"`
}

type WikiCompileSourceRef struct {
	RevisionID string `json:"revision_id"`
	Locator    string `json:"locator"`
}

// WikiCompileCandidate is an unaccepted output. Only RTW AcceptCompile can
// turn a verified object into a maintained, immutable Wiki page revision.
type WikiCompileCandidate struct {
	CompileID     string                 `json:"compile_id"`
	ModuleID      string                 `json:"module_id"`
	PageID        string                 `json:"page_id"`
	BaseRevision  string                 `json:"base_revision_id"`
	Generation    int64                  `json:"generation"`
	InputHash     string                 `json:"input_hash"`
	AttemptID     string                 `json:"attempt_id"`
	LeaseEpoch    int64                  `json:"lease_epoch"`
	CancelVersion int64                  `json:"cancel_version"`
	Title         string                 `json:"title"`
	Markdown      string                 `json:"markdown"`
	ContentSHA256 string                 `json:"content_hash"`
	SourceRefs    []WikiCompileSourceRef `json:"source_refs"`
}

func normalizeWikiCompileInput(in WikiCompileInput) (WikiCompileInput, error) {
	if !wikiCompileID.MatchString(in.CompileID) || !wikiCompileID.MatchString(in.ModuleID) ||
		!validWikiPageID(in.PageID) || !wikiCompileID.MatchString(in.AttemptID) ||
		(in.BaseRevision != "" && !wikiCompileID.MatchString(in.BaseRevision)) ||
		!wikiCompileSHA.MatchString(in.InputHash) || in.Generation < 1 ||
		in.LeaseEpoch < 1 || in.CancelVersion < 0 || len(in.Sources) < 1 ||
		len(in.SourceIDs) != len(in.Sources) ||
		len(in.Sources) > 16 || strings.TrimSpace(in.Guidance) == "" ||
		len(in.Guidance) > 4096 || !utf8.ValidString(in.Guidance) {
		return WikiCompileInput{}, ErrWikiCompileContract
	}
	seen := make(map[string]bool, len(in.Sources))
	selected := append([]string(nil), in.SourceIDs...)
	for _, id := range selected {
		if !wikiCompileID.MatchString(id) {
			return WikiCompileInput{}, ErrWikiCompileContract
		}
	}
	sort.Strings(selected)
	for i := 1; i < len(selected); i++ {
		if selected[i] == selected[i-1] {
			return WikiCompileInput{}, ErrWikiCompileContract
		}
	}
	total := 0
	owned := make([]WikiCompileSource, len(in.Sources))
	for i, source := range in.Sources {
		if !wikiCompileID.MatchString(source.RevisionID) || source.Kind != "source" ||
			seen[source.RevisionID] || strings.TrimSpace(source.Title) == "" ||
			len(source.Title) > 200 || !utf8.ValidString(source.Title) ||
			strings.TrimSpace(source.Content) == "" || !utf8.ValidString(source.Content) ||
			strings.ContainsRune(source.Content, 0) || !wikiCompileSHA.MatchString(source.SHA256) ||
			artifacts.Hash([]byte(source.Content)) != source.SHA256 {
			return WikiCompileInput{}, ErrWikiCompileContract
		}
		total += len(source.Content)
		if total > maxWikiCompileSourceBytes || len(sourceParagraphs(source.Content)) == 0 {
			return WikiCompileInput{}, ErrWikiCompileContract
		}
		seen[source.RevisionID] = true
		owned[i] = source
	}
	for id := range seen {
		position := sort.SearchStrings(selected, id)
		if position >= len(selected) || selected[position] != id {
			return WikiCompileInput{}, ErrWikiCompileContract
		}
	}
	in.SourceIDs = append([]string(nil), in.SourceIDs...)
	in.Sources = owned
	return in, nil
}

func sourceParagraphs(content string) []string {
	// RTW's fixed citationParagraph normalizes CRLF and skips empty blocks, but
	// returns nonempty blocks with their indentation and trailing bytes intact.
	// Trimming the whole source would turn four-space Markdown code into prose.
	parts := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n\n")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			out = append(out, part)
		}
	}
	return out
}

func validateWikiJSONTokens(decoder *json.Decoder, raw []byte, path string, depth int) error {
	if depth > 4 {
		return ErrWikiCompileContract
	}
	token, err := decoder.Token()
	if err != nil {
		return ErrWikiCompileContract
	}
	switch token {
	case json.Delim('{'):
		allowed := map[string]bool{}
		switch path {
		case "":
			allowed = map[string]bool{"title": true, "markdown": true, "source_refs": true}
		case "source_refs[]":
			allowed = map[string]bool{"revision_id": true, "locator": true}
		default:
			return ErrWikiCompileContract
		}
		seen := map[string]bool{}
		for decoder.More() {
			start := decoder.InputOffset()
			key, err := decoder.Token()
			end := decoder.InputOffset()
			name, ok := key.(string)
			literal := []byte(nil)
			if start >= 0 && end <= int64(len(raw)) {
				literal = bytes.TrimSpace(raw[start:end])
			}
			if len(seen) > 0 && len(literal) > 0 && literal[0] == ',' {
				literal = bytes.TrimSpace(literal[1:])
			}
			if err != nil || !ok || !allowed[name] || seen[name] || start < 0 || end > int64(len(raw)) ||
				!bytes.Equal(literal, []byte(`"`+name+`"`)) {
				return ErrWikiCompileContract
			}
			seen[name] = true
			child := name
			if path == "source_refs[]" {
				child = "source_refs[]." + name
			}
			if err := validateWikiJSONTokens(decoder, raw, child, depth+1); err != nil {
				return err
			}
		}
		if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
			return ErrWikiCompileContract
		}
	case json.Delim('['):
		if path != "source_refs" {
			return ErrWikiCompileContract
		}
		for decoder.More() {
			if err := validateWikiJSONTokens(decoder, raw, "source_refs[]", depth+1); err != nil {
				return err
			}
		}
		if closing, err := decoder.Token(); err != nil || closing != json.Delim(']') {
			return ErrWikiCompileContract
		}
	default:
		if path == "" || path == "source_refs" || path == "source_refs[]" {
			return ErrWikiCompileContract
		}
	}
	return nil
}

func checkedWikiSourceRefs(refs []WikiCompileSourceRef, sources []WikiCompileSource) ([]WikiCompileSourceRef, error) {
	if len(refs) < 1 || len(refs) > 64 {
		return nil, ErrWikiCompileContract
	}
	allowed := make(map[string][]string, len(sources))
	for _, source := range sources {
		allowed[source.RevisionID] = sourceParagraphs(source.Content)
	}
	seen := map[WikiCompileSourceRef]bool{}
	owned := make([]WikiCompileSourceRef, len(refs))
	for i, ref := range refs {
		parts := wikiParagraph.FindStringSubmatch(ref.Locator)
		paragraphs, ok := allowed[ref.RevisionID]
		if !ok || len(parts) != 2 || seen[ref] {
			return nil, ErrWikiCompileContract
		}
		position, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || position < 1 || position > int64(len(paragraphs)) {
			return nil, ErrWikiCompileContract
		}
		seen[ref] = true
		owned[i] = ref
	}
	return owned, nil
}

// ParseWikiCompileCandidate rejects duplicate/unknown model keys and forged
// references before a caller may write an object or submit RTW AcceptCompile.
func ParseWikiCompileCandidate(raw string, input WikiCompileInput) (WikiCompileCandidate, error) {
	input, err := normalizeWikiCompileInput(input)
	if err != nil || len(raw) == 0 || len(raw) > maxWikiCompileOutputBytes ||
		!utf8.ValidString(raw) || strings.ContainsRune(raw, 0) {
		return WikiCompileCandidate{}, ErrWikiCompileContract
	}
	tokens := json.NewDecoder(strings.NewReader(raw))
	tokens.UseNumber()
	if validateWikiJSONTokens(tokens, []byte(raw), "", 0) != nil {
		return WikiCompileCandidate{}, ErrWikiCompileContract
	}
	if _, err := tokens.Token(); err != io.EOF {
		return WikiCompileCandidate{}, ErrWikiCompileContract
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.DisallowUnknownFields()
	var proposal struct {
		Title      string                 `json:"title"`
		Markdown   string                 `json:"markdown"`
		SourceRefs []WikiCompileSourceRef `json:"source_refs"`
	}
	if decoder.Decode(&proposal) != nil || decoder.Decode(new(any)) != io.EOF ||
		strings.TrimSpace(proposal.Title) == "" || len(proposal.Title) > 200 ||
		strings.ContainsRune(proposal.Title, 0) ||
		strings.TrimSpace(proposal.Markdown) == "" || len(proposal.Markdown) > 48<<10 ||
		strings.ContainsRune(proposal.Markdown, 0) || len(proposal.SourceRefs) < 1 ||
		len(proposal.SourceRefs) > 64 {
		return WikiCompileCandidate{}, ErrWikiCompileContract
	}
	refs, err := checkedWikiSourceRefs(proposal.SourceRefs, input.Sources)
	if err != nil {
		return WikiCompileCandidate{}, err
	}
	return WikiCompileCandidate{CompileID: input.CompileID, ModuleID: input.ModuleID,
		PageID: input.PageID, BaseRevision: input.BaseRevision, Generation: input.Generation,
		InputHash: input.InputHash, AttemptID: input.AttemptID, LeaseEpoch: input.LeaseEpoch,
		CancelVersion: input.CancelVersion, Title: strings.TrimSpace(proposal.Title),
		Markdown: proposal.Markdown, ContentSHA256: artifacts.Hash([]byte(proposal.Markdown)),
		SourceRefs: refs}, nil
}
