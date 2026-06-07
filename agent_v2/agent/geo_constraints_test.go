package agent

import "testing"

func TestTravelGeoConstraintRejectsBeijingTianjinForSouthwest(t *testing.T) {
	req := TravelRequirementSnapshot{
		StartCity:        "丽江",
		DestinationScope: "西南地区",
		TotalDays:        30,
		TransportMode:    "自驾",
	}
	constraint := buildTravelGeoConstraint(req, "丽江出发，在西南地区一个月")
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
	req := TravelRequirementSnapshot{
		StartCity:        "北京",
		DestinationScope: "西南地区",
		TotalDays:        30,
	}
	constraint := buildTravelGeoConstraint(req, "北京出发，西南地区一个月")
	if violation := constraint.CheckText("北京出发至成都", "北京出发至成都", true); violation != nil {
		t.Fatalf("start city should be exempt in transfer description: %#v", violation)
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
