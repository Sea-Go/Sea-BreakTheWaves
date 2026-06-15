package stages

import (
	"agent_v3/internal/graph"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"trpc.group/trpc-go/trpc-agent-go/log"
)

func extractDayIDs(overview *graph.TripOverview) []string {
	var ids []string
	for _, d := range overview.Days {
		if id, ok := d["id"].(string); ok && id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func extractWeekIDs(overview *graph.TripOverview) []string {
	var ids []string
	for _, w := range overview.Weeks {
		if id, ok := w["id"].(string); ok && id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func getStr(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getFloat(m map[string]any, key string) float64 {
	if v, ok := m[key]; ok {
		switch val := v.(type) {
		case float64:
			return val
		case int64:
			return float64(val)
		case int:
			return float64(val)
		}
	}
	return 0
}

func monthFromDate(dateStr string) int {
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return 1
	}
	return int(t.Month())
}

func deduplicate(items []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, item := range items {
		if item != "" && !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}

func stringFromAny(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func defaultIfEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func parseAmapLngLat(location string) (float64, float64, error) {
	parts := strings.Split(strings.TrimSpace(location), ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid location %q", location)
	}
	lng, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, err
	}
	lat, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, err
	}
	return lng, lat, nil
}

func isValidLngLat(lng, lat float64) bool {
	if math.IsNaN(lng) || math.IsNaN(lat) || math.IsInf(lng, 0) || math.IsInf(lat, 0) {
		return false
	}
	if lng == 0 && lat == 0 {
		return false
	}
	return lng >= -180 && lng <= 180 && lat >= -90 && lat <= 90
}

// --- Debug ---

type workflowDebugSnapshot struct {
	RunID              string `json:"run_id"`
	UserID             string `json:"user_id"`
	SessionID          string `json:"session_id"`
	RequestID          string `json:"request_id"`
	ExpectedTripPlanID string `json:"expected_trip_plan_id"`
	Stage              string `json:"stage"`
	OutputLen          int    `json:"output_len"`
	OutputHead         string `json:"output_head"`
	OutputTail         string `json:"output_tail"`
	ExtractedID        string `json:"extracted_trip_plan_id"`
	Error              string `json:"error"`
	CreatedAt          string `json:"created_at"`
}

func (a *graphWorkflowAgent) saveWorkflowDebugSnapshot(
	userID, sessionID, requestID, expectedTripPlanID, stage, output, errMsg string,
) {
	head := output
	if len(head) > 2000 {
		head = head[:2000]
	}
	tail := output
	if len(tail) > 2000 {
		tail = tail[len(tail)-2000:]
	}

	snap := workflowDebugSnapshot{
		RunID:              requestID,
		UserID:             userID,
		SessionID:          sessionID,
		RequestID:          requestID,
		ExpectedTripPlanID: expectedTripPlanID,
		Stage:              stage,
		OutputLen:          len(output),
		OutputHead:         head,
		OutputTail:         tail,
		ExtractedID:        extractTripPlanID(output),
		Error:              errMsg,
		CreatedAt:          time.Now().Format(time.RFC3339),
	}

	dir := "/tmp/sea_workflow_debug"
	_ = os.MkdirAll(dir, 0o755)
	filename := filepath.Join(dir, fmt.Sprintf("%s_%s_%d.json", requestID, stage, time.Now().Unix()))
	b, _ := json.MarshalIndent(snap, "", "  ")
	if err := os.WriteFile(filename, b, 0o644); err != nil {
		log.Errorf("[workflow-runner] save debug snapshot failed: %v", err)
	} else {
		log.Infof("[workflow-runner] debug snapshot saved: %s", filename)
	}
}

// --- Graph Splitting: Phase → Month → Week → Day (mechanical, no LLM) ---
