package stages

import "testing"

func TestMinMacroPhaseCountScalesWithTripLength(t *testing.T) {
	cases := []struct {
		name      string
		totalDays int
		want      int
	}{
		{name: "unknown defaults to long trip rule", totalDays: 0, want: 3},
		{name: "one day trip", totalDays: 1, want: 1},
		{name: "two day trip", totalDays: 2, want: 1},
		{name: "short trip", totalDays: 3, want: 2},
		{name: "medium trip", totalDays: 5, want: 2},
		{name: "long trip", totalDays: 6, want: 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := minMacroPhaseCount(tc.totalDays); got != tc.want {
				t.Fatalf("minMacroPhaseCount(%d) = %d, want %d", tc.totalDays, got, tc.want)
			}
		})
	}
}
