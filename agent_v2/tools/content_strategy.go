package tools

import (
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

var contentStrategyMemoryDir = filepath.Join("data", "content_strategy_memory")

type ContentStrategyMemory struct {
	PreferredStructure  []string `json:"preferred_structure"`
	DoMore              []string `json:"do_more"`
	Avoid               []string `json:"avoid"`
	UnansweredQuestions []string `json:"unanswered_questions"`
}

func LoadContentStrategyMemory(key string) (ContentStrategyMemory, error) {
	for _, path := range contentStrategyMemoryPaths(key) {
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return ContentStrategyMemory{}, err
		}

		var memory ContentStrategyMemory
		if err := json.Unmarshal(data, &memory); err != nil {
			return ContentStrategyMemory{}, err
		}
		return NormalizeContentStrategyMemory(memory), nil
	}
	return ContentStrategyMemory{}, nil
}

func SaveContentStrategyMemory(key string, memory ContentStrategyMemory) (string, error) {
	memory = NormalizeContentStrategyMemory(memory)
	path := contentStrategyMemoryWritePath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}

	data, err := json.MarshalIndent(memory, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func MergeContentStrategyMemory(base, update ContentStrategyMemory) ContentStrategyMemory {
	return NormalizeContentStrategyMemory(ContentStrategyMemory{
		PreferredStructure:  append(append([]string{}, base.PreferredStructure...), update.PreferredStructure...),
		DoMore:              append(append([]string{}, base.DoMore...), update.DoMore...),
		Avoid:               append(append([]string{}, base.Avoid...), update.Avoid...),
		UnansweredQuestions: append(append([]string{}, base.UnansweredQuestions...), update.UnansweredQuestions...),
	})
}

func NormalizeContentStrategyMemory(memory ContentStrategyMemory) ContentStrategyMemory {
	memory.PreferredStructure = normalizeStringList(memory.PreferredStructure)
	memory.DoMore = normalizeStringList(memory.DoMore)
	memory.Avoid = normalizeStringList(memory.Avoid)
	memory.UnansweredQuestions = normalizeStringList(memory.UnansweredQuestions)
	return memory
}

func contentStrategyMemoryPath(key string) string {
	key = sanitizeContentKey(key)
	return filepath.Join(contentStrategyMemoryDir, key+".json")
}

func contentStrategyMemoryPaths(key string) []string {
	legacy := filepath.Join(contentStrategyMemoryDir, legacyFileStem(key)+".json")
	current := contentStrategyMemoryPath(key)
	if legacy == current {
		return []string{current}
	}
	return []string{legacy, current}
}

func contentStrategyMemoryWritePath(key string) string {
	paths := contentStrategyMemoryPaths(key)
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return paths[len(paths)-1]
}

func sanitizeContentKey(key string) string {
	original := strings.TrimSpace(key)
	if original == "" {
		original = "default"
	}

	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(original) {
		isSafe := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isSafe {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}

	prefix := strings.Trim(b.String(), "_")
	if prefix == "" {
		prefix = "key"
	}

	sum := sha1.Sum([]byte(original))
	return fmt.Sprintf("%s_%x", prefix, sum[:6])
}

func legacyFileStem(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "default"
	}

	var b strings.Builder
	lastUnderscore := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
			lastUnderscore = false
		case r == '_' || r == '-':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}

	out := strings.Trim(b.String(), "_-.")
	if out == "" {
		return "default"
	}
	return out
}

func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
