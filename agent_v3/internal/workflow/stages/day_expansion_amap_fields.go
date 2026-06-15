package stages

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func amapTextField(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case []string:
		return strings.TrimSpace(strings.Join(v, " "))
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			text := strings.TrimSpace(fmt.Sprint(item))
			if text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func numberFromAmapField(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		out, _ := v.Float64()
		return out
	case string:
		out, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return out
	default:
		return 0
	}
}

func defaultPOIDuration(kind string) int {
	switch kind {
	case "餐饮":
		return 60
	case "住宿":
		return 0
	default:
		return 120
	}
}

func defaultPOICost(kind string) float64 {
	switch kind {
	case "餐饮":
		return 80
	case "住宿":
		return 400
	default:
		return 50
	}
}
