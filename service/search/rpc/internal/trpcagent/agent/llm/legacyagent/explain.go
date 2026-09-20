package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// explainBuilder builds a multi-step explanation string + structured trace.
// Design goals:
// - Keep RecommendResponse.Explanation human-readable, multi-line.
// - Keep structured trace for programmatic inspection (ExplainTrace).
// - Redact potentially sensitive large texts by default.
// - Apply a max length cap to avoid huge responses.

type explainStep struct {
	Name string         `json:"name"`
	Data map[string]any `json:"data,omitempty"`
}

type explainBuilder struct {
	steps       []explainStep
	maxChars    int
	redactText  bool
	redactLimit int
}

func newExplainBuilder(explain bool) *explainBuilder {
	// Note: we still build even when explain=false, but keep it minimal.
	maxChars := 16000
	if !explain {
		maxChars = 1024
	}
	return &explainBuilder{
		maxChars:    maxChars,
		redactText:  true,
		redactLimit: 120,
	}
}

func (b *explainBuilder) Add(name string, data map[string]any) {
	if name == "" {
		name = "step"
	}
	if data == nil {
		data = map[string]any{}
	}
	if b.redactText {
		data = redactMap(data, b.redactLimit)
	}
	b.steps = append(b.steps, explainStep{Name: name, Data: data})
}

func (b *explainBuilder) Text() string {
	var sb strings.Builder
	for i, st := range b.steps {
		sb.WriteString(fmt.Sprintf("[%02d] %s\n", i+1, st.Name))
		if len(st.Data) == 0 {
			sb.WriteString("  (no data)\n")
			continue
		}
		js, err := json.Marshal(st.Data)
		if err != nil {
			sb.WriteString("  (marshal_error)\n")
			continue
		}
		sb.WriteString("  ")
		sb.WriteString(string(js))
		sb.WriteString("\n")
	}
	return clampString(sb.String(), b.maxChars)
}

func (b *explainBuilder) Trace() []explainStep {
	return b.steps
}

func clampString(s string, max int) string {
	if max <= 0 {
		return s
	}
	if len(s) <= max {
		return s
	}
	head := max * 2 / 3
	tail := max - head
	if head < 0 {
		head = 0
	}
	if tail < 0 {
		tail = 0
	}
	return s[:head] + "\n...(truncated)...\n" + s[len(s)-tail:]
}

func redactMap(in map[string]any, limit int) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		lk := strings.ToLower(k)
		switch vv := v.(type) {
		case string:
			// Redact long free-form texts. Keep short values as-is.
			if lk == "query" || lk == "user_query" || lk == "long_mem" || lk == "short_mem" || lk == "periodic_mem" || lk == "long_hint" || lk == "short_hint" || lk == "periodic_hint" {
				out[k] = fmt.Sprintf("<redacted:%d chars>", len(vv))
				continue
			}
			if limit > 0 && len(vv) > limit {
				out[k] = vv[:limit] + "...(truncated)"
			} else {
				out[k] = vv
			}
		default:
			out[k] = v
		}
	}
	return out
}
