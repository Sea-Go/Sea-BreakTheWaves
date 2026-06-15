package geo

import (
	"testing"

	domaintravel "agent_v3/internal/domain/travel"
)

func TestTravelGeoConstraintRejectsBeijingTianjinForSouthwest(t *testing.T) {
	req := domaintravel.TravelRequirementSnapshot{
		StartCity:        "丽江",
		DestinationScope: "西南地区",
		TotalDays:        30,
		TransportMode:    "自驾",
	}
	constraint := BuildTravelGeoConstraint(req, "丽江出发，在西南地区一个月")
	if !constraint.Enabled {
		t.Fatal("constraint should be enabled for southwest scope")
	}

	for _, phase := range []string{"天津城市探索", "北京文化体验"} {
		if violation := constraint.CheckText(phase, phase, true); violation == nil {
			t.Fatalf("phase %q should be rejected for southwest scope", phase)
		}
	}

	for _, phase := range []string{"丽江滇西北自然风光", "香格里拉梅里雪山", "林芝南迦巴瓦"} {
		if violation := constraint.CheckText(phase, phase, true); violation != nil {
			t.Fatalf("phase %q should be allowed, got %#v", phase, violation)
		}
	}
}

func TestTravelGeoConstraintAllowsStartCityOnlyAsStart(t *testing.T) {
	req := domaintravel.TravelRequirementSnapshot{
		StartCity:        "北京",
		DestinationScope: "西南地区",
		TotalDays:        30,
	}
	constraint := BuildTravelGeoConstraint(req, "北京出发，西南地区一个月")
	if violation := constraint.CheckText("北京出发至成都", "北京出发至成都", true); violation != nil {
		t.Fatalf("start city should be exempt in transfer description: %#v", violation)
	}
	if violation := constraint.CheckText("北京出发城市景点", "北京出发城市景点", true); violation == nil {
		t.Fatal("start city should not be exempt when it is planned as a visit")
	}
	if violation := constraint.CheckText("北京文化体验", "北京文化体验", true); violation == nil {
		t.Fatal("start city should not become an allowed destination phase")
	}
	if violation := constraint.CheckText("北京景点", "北京景点", false); violation == nil {
		t.Fatal("start city should not become an allowed POI")
	}
	if violation := constraint.CheckText("天津顺路游", "天津顺路游", true); violation == nil {
		t.Fatal("non-start out-of-scope city should still be rejected")
	}
}

func TestTravelGeoConstraintAllowsStartCityProvinceOnlyForLogistics(t *testing.T) {
	req := domaintravel.TravelRequirementSnapshot{
		StartCity:        "昆明",
		DestinationScope: "香格里拉",
		MustVisit:        []string{"香格里拉"},
		TotalDays:        7,
	}
	constraint := BuildTravelGeoConstraint(req, "昆明出发，香格里拉雪山7日自驾")
	if !constraint.Enabled {
		t.Fatal("constraint should be enabled for explicit destination")
	}

	if violation := constraint.CheckText("昆明", "昆明 昆明出发与滇中过渡", true); violation != nil {
		t.Fatalf("start city province alias should be allowed in logistics phase: %#v", violation)
	}
	if violation := constraint.CheckText("昆明", "昆明 昆明出发去云南旅游", true); violation == nil {
		t.Fatal("start city province alias should not become an allowed destination")
	}
}

func TestTravelGeoConstraintAllowsStartCityRouteEdges(t *testing.T) {
	req := domaintravel.TravelRequirementSnapshot{
		StartCity:        "南昌",
		DestinationScope: "上海",
		TotalDays:        7,
	}
	constraint := BuildTravelGeoConstraint(req, "从南昌，玩7天，目的地上海")

	for _, phase := range []string{"南昌-上海 南昌至上海交通衔接", "上海-南昌 返程准备与离开"} {
		if violation := constraint.CheckText(phase, phase, true); violation != nil {
			t.Fatalf("route edge %q should allow start city logistics, got %#v", phase, violation)
		}
	}
	if violation := constraint.CheckText("南昌历史文化探索", "南昌历史文化探索", true); violation == nil {
		t.Fatal("start city should still be rejected as a destination phase")
	}
}
