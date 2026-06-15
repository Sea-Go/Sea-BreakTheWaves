package trace

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

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
