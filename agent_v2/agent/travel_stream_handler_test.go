package agent

import (
	"strings"
	"testing"
)

func TestUserMessageFromHistoryPreservesRoleOrderedTranscript(t *testing.T) {
	transcript := userMessageFromHistory([]travelStreamMessage{
		{Role: "user", Content: "我想去上海玩"},
		{Role: "agent", Content: "你计划从哪个城市出发？"},
		{Role: "user", Content: "南昌，无所谓，无所谓"},
	})

	for _, want := range []string{"用户第1轮：我想去上海玩", "Agent第1轮：你计划从哪个城市出发？", "用户第2轮：南昌，无所谓，无所谓"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript missing %q: %s", want, transcript)
		}
	}
	if latest := latestUserTurnText(transcript); latest != "南昌，无所谓，无所谓" {
		t.Fatalf("latest user turn = %q", latest)
	}
	turns := extractUserTurnTexts(transcript)
	if len(turns) != 2 || turns[0] != "我想去上海玩" || turns[1] != "南昌，无所谓，无所谓" {
		t.Fatalf("user turns not recovered: %#v", turns)
	}
}
