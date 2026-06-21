package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"agent_v2/graph"
)

type TravelGeoConstraint struct {
	Enabled         bool
	ScopeText       string
	StartCity       string
	AllowedKeywords []string
}

type TravelGeoViolation struct {
	Label          string
	Text           string
	MatchedKeyword string
	ScopeText      string
	Reason         string
}

type TravelGeoScopeError struct {
	Violations []TravelGeoViolation
}

func (e *TravelGeoScopeError) Error() string {
	if e == nil || len(e.Violations) == 0 {
		return "目的地范围校验失败"
	}
	labels := make([]string, 0, len(e.Violations))
	for _, violation := range e.Violations {
		labels = append(labels, violation.Label)
	}
	return "目的地范围越界：" + strings.Join(labels, "、")
}

type geoRegionRule struct {
	Name    string
	Aliases []string
	Allows  []string
}

var travelGeoRegionRules = []geoRegionRule{
	{
		Name:    "西南",
		Aliases: []string{"西南", "西南地区", "云贵川", "云贵川渝", "云贵川渝藏", "川滇藏", "滇藏"},
		Allows: []string{
			"西南", "云南", "贵州", "四川", "重庆", "西藏", "滇", "黔", "川", "渝", "藏",
			"昆明", "丽江", "大理", "滇西北", "香格里拉", "迪庆", "德钦", "梅里", "泸沽湖",
			"成都", "阿坝", "甘孜", "稻城", "亚丁", "康定", "四姑娘山",
			"贵阳", "遵义", "黔东南", "荔波", "黄果树",
			"拉萨", "林芝", "鲁朗", "巴松措", "南迦巴瓦", "雅鲁藏布",
		},
	},
	{
		Name:    "华北",
		Aliases: []string{"华北", "京津冀"},
		Allows:  []string{"华北", "北京", "天津", "河北", "石家庄", "保定", "承德", "秦皇岛", "张家口", "山西", "太原", "大同", "内蒙古", "呼和浩特"},
	},
	{
		Name:    "东北",
		Aliases: []string{"东北", "东三省"},
		Allows:  []string{"东北", "辽宁", "沈阳", "大连", "吉林", "长春", "黑龙江", "哈尔滨", "漠河", "延吉", "长白山"},
	},
	{
		Name:    "华东",
		Aliases: []string{"华东", "江浙沪", "长三角"},
		Allows:  []string{"华东", "上海", "江苏", "南京", "苏州", "无锡", "浙江", "杭州", "宁波", "安徽", "合肥", "黄山", "福建", "福州", "厦门", "江西", "南昌", "山东", "济南", "青岛"},
	},
	{
		Name:    "华南",
		Aliases: []string{"华南", "岭南", "两广"},
		Allows:  []string{"华南", "广东", "广州", "深圳", "珠海", "广西", "桂林", "南宁", "北海", "海南", "海口", "三亚", "香港", "澳门"},
	},
	{
		Name:    "华中",
		Aliases: []string{"华中", "中部"},
		Allows:  []string{"华中", "河南", "郑州", "洛阳", "湖北", "武汉", "宜昌", "湖南", "长沙", "张家界", "湘西"},
	},
	{
		Name:    "西北",
		Aliases: []string{"西北", "大西北"},
		Allows:  []string{"西北", "陕西", "西安", "甘肃", "兰州", "敦煌", "青海", "西宁", "新疆", "乌鲁木齐", "喀什", "伊犁", "宁夏", "银川"},
	},
}

var standaloneGeoRules = []geoRegionRule{
	{Name: "云南", Aliases: []string{"云南", "滇"}, Allows: []string{"云南", "滇", "昆明", "丽江", "大理", "香格里拉", "迪庆", "德钦", "梅里", "泸沽湖", "西双版纳", "腾冲"}},
	{Name: "四川", Aliases: []string{"四川", "川西", "川"}, Allows: []string{"四川", "川", "川西", "成都", "阿坝", "甘孜", "稻城", "亚丁", "康定", "四姑娘山", "九寨沟"}},
	{Name: "贵州", Aliases: []string{"贵州", "黔"}, Allows: []string{"贵州", "黔", "贵阳", "遵义", "黔东南", "荔波", "黄果树"}},
	{Name: "重庆", Aliases: []string{"重庆", "渝"}, Allows: []string{"重庆", "渝", "武隆"}},
	{Name: "西藏", Aliases: []string{"西藏", "藏东南", "藏"}, Allows: []string{"西藏", "藏", "拉萨", "林芝", "鲁朗", "巴松措", "南迦巴瓦", "雅鲁藏布"}},
	{Name: "北京", Aliases: []string{"北京"}, Allows: []string{"北京"}},
	{Name: "天津", Aliases: []string{"天津"}, Allows: []string{"天津"}},
	{Name: "河北", Aliases: []string{"河北"}, Allows: []string{"河北", "石家庄", "保定", "承德", "秦皇岛", "张家口"}},
	{Name: "上海", Aliases: []string{"上海"}, Allows: []string{"上海"}},
	{Name: "浙江", Aliases: []string{"浙江"}, Allows: []string{"浙江", "杭州", "宁波", "温州", "绍兴"}},
	{Name: "江苏", Aliases: []string{"江苏"}, Allows: []string{"江苏", "南京", "苏州", "无锡", "扬州"}},
	{Name: "广东", Aliases: []string{"广东"}, Allows: []string{"广东", "广州", "深圳", "珠海", "佛山"}},
	{Name: "广西", Aliases: []string{"广西"}, Allows: []string{"广西", "桂林", "南宁", "北海"}},
	{Name: "新疆", Aliases: []string{"新疆"}, Allows: []string{"新疆", "乌鲁木齐", "喀什", "伊犁", "阿勒泰"}},
}

func buildTravelGeoConstraint(req TravelRequirementSnapshot, extraText string) TravelGeoConstraint {
	destinationIntentText := strings.Join([]string{
		req.DestinationScope,
		strings.Join(req.MustVisit, " "),
	}, " ")
	scopeText := strings.Join([]string{
		destinationIntentText,
		extraText,
	}, " ")
	for _, anchor := range req.DestinationAnchors {
		scopeText += " " + anchor.Destination + " " + anchor.Name
		destinationIntentText += " " + anchor.Destination + " " + anchor.Name
	}
	if strings.TrimSpace(scopeText) == "" || containsAny(scopeText, []string{"全国", "全中国", "国内", "中国大陆", "不限区域"}) {
		return TravelGeoConstraint{ScopeText: scopeText, StartCity: req.StartCity}
	}

	allowed := []string{}
	for _, rule := range append(travelGeoRegionRules, standaloneGeoRules...) {
		if containsAny(scopeText, append([]string{rule.Name}, rule.Aliases...)) {
			allowed = append(allowed, rule.Allows...)
		}
	}
	for _, anchor := range req.DestinationAnchors {
		allowed = append(allowed, anchor.Destination, anchor.Name)
	}
	allowed = append(allowed, req.MustVisit...)

	allowed = compactUniqueStrings(allowed)
	if req.StartCity != "" &&
		!containsAny(destinationIntentText, []string{req.StartCity}) &&
		!destinationIntentAllowsKeyword(destinationIntentText, req.StartCity) {
		allowed = removeGeoKeywordFamily(allowed, req.StartCity)
	}
	return TravelGeoConstraint{
		Enabled:         len(allowed) > 0,
		ScopeText:       scopeText,
		StartCity:       req.StartCity,
		AllowedKeywords: allowed,
	}
}

func buildTravelGeoConstraintFromOverview(overview *graph.TripOverview) TravelGeoConstraint {
	if overview == nil {
		return TravelGeoConstraint{}
	}
	req := TravelRequirementSnapshot{
		DestinationScope: strings.Join(append([]string{
			overview.TripPlan.Name,
			overview.TripPlan.RawRequirements,
		}, overview.TripPlan.MustVisit...), " "),
		TotalDays:     overview.TripPlan.TotalDays,
		TransportMode: overview.TripPlan.TransportMode,
		TravelStyle:   append([]string{overview.TripPlan.TravelStyle}, overview.TripPlan.Interests...),
		MustVisit:     append([]string(nil), overview.TripPlan.MustVisit...),
	}
	enrichRequirementPlanningAnchors(&req)
	return buildTravelGeoConstraint(req, overview.TripPlan.RawRequirements)
}

func (c TravelGeoConstraint) CheckText(label, text string, allowStartCity bool) *TravelGeoViolation {
	text = strings.TrimSpace(text)
	if !c.Enabled || text == "" {
		return nil
	}
	for _, keyword := range knownGeoKeywords() {
		if keyword == "" || !strings.Contains(text, keyword) {
			continue
		}
		if keyword == "西北" && strings.Contains(text, "滇西北") && c.keywordAllowed("滇西北") {
			continue
		}
		if c.startCityLogisticsKeywordAllowed(keyword, text, allowStartCity) {
			continue
		}
		if c.keywordAllowed(keyword) {
			continue
		}
		return &TravelGeoViolation{
			Label:          defaultIfEmpty(label, keyword),
			Text:           text,
			MatchedKeyword: keyword,
			ScopeText:      c.ScopeText,
			Reason:         fmt.Sprintf("「%s」不属于本次目的地范围。", keyword),
		}
	}
	return nil
}

func (c TravelGeoConstraint) startCityLogisticsKeywordAllowed(keyword, text string, allowStartCity bool) bool {
	if !allowStartCity || c.StartCity == "" || !isStartCityTransferMention(text, c.StartCity) {
		return false
	}
	if keyword == c.StartCity {
		return !isStartCityDestinationMention(text, c.StartCity)
	}
	return isStartCityGeoFamilyKeyword(keyword, c.StartCity) && isStartCityLogisticsContext(text)
}

func removeGeoKeywordFamily(values []string, keyword string) []string {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return values
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == keyword || strings.Contains(value, keyword) || strings.Contains(keyword, value) {
			continue
		}
		out = append(out, value)
	}
	return out
}

func destinationIntentAllowsKeyword(destinationIntentText, keyword string) bool {
	destinationIntentText = strings.TrimSpace(destinationIntentText)
	keyword = strings.TrimSpace(keyword)
	if destinationIntentText == "" || keyword == "" {
		return false
	}
	for _, rule := range append(travelGeoRegionRules, standaloneGeoRules...) {
		if !containsAny(destinationIntentText, append([]string{rule.Name}, rule.Aliases...)) {
			continue
		}
		for _, allowed := range rule.Allows {
			if keyword == allowed || strings.Contains(allowed, keyword) || strings.Contains(keyword, allowed) {
				return true
			}
		}
	}
	return false
}

func isStartCityTransferMention(text, startCity string) bool {
	if startCity == "" || !strings.Contains(text, startCity) {
		return false
	}
	return containsAny(text, []string{
		startCity + "出发",
		startCity + "出发地",
		"从" + startCity,
		"由" + startCity,
		startCity + "启程",
		startCity + "起点",
		"起点" + startCity,
		"出发地" + startCity,
		startCity + "集结",
		startCity + "集合",
		startCity + "转场",
		startCity + "中转",
		startCity + "接驳",
		startCity + "交通",
		startCity + "至",
		startCity + "到",
		startCity + "-",
		startCity + "—",
		"抵达" + startCity,
		"到达" + startCity,
		"经" + startCity,
		"途经" + startCity,
		"至" + startCity,
		"到" + startCity,
		"-" + startCity,
		"—" + startCity,
		"返程" + startCity,
		"返回" + startCity,
		"回到" + startCity,
		"离开" + startCity,
	})
}

func isStartCityDestinationMention(text, startCity string) bool {
	if startCity == "" || !strings.Contains(text, startCity) {
		return false
	}
	if containsAny(text, []string{
		"旅游", "景点", "景区", "市区", "老城", "城区", "文化体验",
		"自然风光", "深度游", "游览", "核心体验",
	}) && !containsAny(text, []string{
		"至", "到", "-", "—", "交通", "衔接", "过渡", "转场", "中转", "接驳",
	}) {
		return true
	}
	return containsAny(text, []string{
		startCity + "景点",
		startCity + "市区",
		startCity + "老城",
		startCity + "城区",
		startCity + "文化",
		startCity + "体验",
		startCity + "游览",
		startCity + "深度",
		startCity + "停留",
		startCity + "核心",
		startCity + "自然风光",
		"在" + startCity + "玩",
		"在" + startCity + "游",
	})
}

func isStartCityLogisticsContext(text string) bool {
	if !containsAny(text, []string{
		"出发", "启程", "起点", "出发地", "集结", "集合", "转场", "中转",
		"接驳", "交通", "衔接", "过渡", "抵达", "到达", "途经", "返程",
		"返回", "回到", "离开",
	}) {
		return false
	}
	return !containsAny(text, []string{
		"旅游", "旅行", "景点", "景区", "市区", "老城", "城区", "文化体验",
		"自然风光", "深度游", "游览", "核心体验",
	})
}

func isStartCityGeoFamilyKeyword(keyword, startCity string) bool {
	keyword = strings.TrimSpace(keyword)
	startCity = strings.TrimSpace(startCity)
	if keyword == "" || startCity == "" {
		return false
	}
	for _, rule := range standaloneGeoRules {
		names := append([]string{rule.Name}, rule.Aliases...)
		if !stringSliceContainsExact(append(names, rule.Allows...), startCity) {
			continue
		}
		if stringSliceContainsExact(append(names, startCity), keyword) {
			return true
		}
	}
	return false
}

func (c TravelGeoConstraint) CheckPOI(poi graph.POIInput) *TravelGeoViolation {
	text := strings.Join([]string{poi.City, poi.District, poi.Name}, " ")
	if violation := c.CheckText(poi.Name, text, false); violation != nil {
		violation.Text = strings.Join([]string{poi.Name, poi.City, poi.District, poi.Address}, " ")
		return violation
	}
	return nil
}

func (c TravelGeoConstraint) CheckPOINode(poi graph.POINode) *TravelGeoViolation {
	text := strings.Join([]string{poi.City, poi.District, poi.Name}, " ")
	if violation := c.CheckText(poi.Name, text, false); violation != nil {
		violation.Text = strings.Join([]string{poi.Name, poi.City, poi.District, poi.Address}, " ")
		return violation
	}
	return nil
}

func (c TravelGeoConstraint) keywordAllowed(keyword string) bool {
	for _, allowed := range c.AllowedKeywords {
		if allowed == "" {
			continue
		}
		if keyword == allowed || strings.Contains(allowed, keyword) || strings.Contains(keyword, allowed) {
			return true
		}
	}
	return false
}

func knownGeoKeywords() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, rule := range append(travelGeoRegionRules, standaloneGeoRules...) {
		for _, value := range append(append([]string{rule.Name}, rule.Aliases...), rule.Allows...) {
			value = strings.TrimSpace(value)
			if value == "" || seen[value] {
				continue
			}
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return len([]rune(out[i])) > len([]rune(out[j]))
	})
	return out
}

func compactUniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func stringSliceContainsExact(values []string, want string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}

func emitGeoScopeViolationAnnotation(ctx context.Context, emitter *TraceEmitter, level, nodeID string, violation TravelGeoViolation) {
	if emitter == nil {
		return
	}
	emitter.Emit(ctx, PublicPlanningEvent{
		Type:           EventMapAnnotationAdded,
		Level:          defaultIfEmpty(level, "overview"),
		NodeID:         nodeID,
		Status:         "rejected",
		PublicAction:   "审核目的地范围",
		ThoughtSummary: "发现规划内容偏离用户指定的目的地范围，已阻断继续展开。",
		RecordedFacts:  []string{fmt.Sprintf("%s：%s", violation.Label, violation.Reason)},
		Annotation: &PublicMapAnnotation{
			ID:       stablePlanningAnnotationID("geo-scope", nodeID, violation.Label, violation.MatchedKeyword),
			Kind:     "review",
			Source:   "review",
			Title:    "范围越界审核",
			Summary:  fmt.Sprintf("%s：%s", violation.Label, violation.Reason),
			Status:   "rejected",
			Tags:     []string{"范围", "审核", "越界"},
			Reasons:  []string{"用户已限定目的地范围，规划不能引入明显无关城市。"},
			Evidence: []string{truncateGuideText(violation.Text, maxGuideAnnotationSummary)},
			Anchor: PublicMapAnnotationAnchor{
				Type:   "scope",
				NodeID: nodeID,
				Label:  defaultIfEmpty(violation.Label, "范围审核"),
			},
		},
	})
}

func emitGeoScopeViolationAnnotations(ctx context.Context, emitter *TraceEmitter, level, nodeID string, violations []TravelGeoViolation) {
	for _, violation := range violations {
		emitGeoScopeViolationAnnotation(ctx, emitter, level, nodeID, violation)
	}
}

func filterPOIsByGeoConstraint(ctx context.Context, emitter *TraceEmitter, dayID string, pois []graph.POIInput, constraint TravelGeoConstraint) []graph.POIInput {
	if !constraint.Enabled || len(pois) == 0 {
		return pois
	}
	out := make([]graph.POIInput, 0, len(pois))
	for _, poi := range pois {
		if violation := constraint.CheckPOI(poi); violation != nil {
			emitGeoScopeViolationAnnotation(ctx, emitter, "day", dayID, *violation)
			continue
		}
		out = append(out, poi)
	}
	return out
}

func (a *graphWorkflowAgent) reviewGeoScope(ctx context.Context, tripPlanID string, overview *graph.TripOverview, trace *TraceEmitter, requirements ...TravelRequirementSnapshot) error {
	constraint := buildTravelGeoConstraintFromOverview(overview)
	if len(requirements) > 0 {
		candidate := buildTravelGeoConstraint(requirements[0], overview.TripPlan.RawRequirements)
		if candidate.Enabled {
			constraint = candidate
		}
	}
	if !constraint.Enabled {
		return nil
	}

	var violations []TravelGeoViolation
	for _, phase := range overview.Phases {
		label := firstNonEmptyString(getStr(phase, "region"), getStr(phase, "name"))
		text := strings.Join([]string{getStr(phase, "region"), getStr(phase, "name"), getStr(phase, "theme")}, " ")
		if violation := constraint.CheckText(label, text, true); violation != nil {
			violations = append(violations, *violation)
		}
	}
	for _, day := range overview.Days {
		dayID := getStr(day, "id")
		if dayID == "" {
			continue
		}
		subgraph, err := a.graphClient.GetDaySubgraph(ctx, dayID)
		if err != nil || subgraph == nil {
			continue
		}
		for _, poi := range subgraph.POIs {
			if violation := constraint.CheckPOINode(poi); violation != nil {
				violations = append(violations, *violation)
			}
		}
	}
	if len(violations) == 0 {
		review := graph.ReviewInput{
			Level:       "trip",
			Dimension:   "geo_scope",
			Score:       95,
			Passed:      true,
			Summary:     "目的地范围审核通过，未发现明显越界城市。",
			Suggestions: []string{"继续保持路线围绕用户指定区域展开。"},
		}
		_, _ = a.graphClient.WriteReviewResult(ctx, tripPlanID, review)
		emitReviewAnnotation(ctx, trace, "overview", tripPlanID, "范围审核", "范围审核", "scope", review)
		return nil
	}

	emitGeoScopeViolationAnnotations(ctx, trace, "overview", tripPlanID, violations)
	issues := make([]string, 0, len(violations))
	for _, violation := range violations {
		issues = append(issues, fmt.Sprintf("%s 命中 %s", violation.Label, violation.MatchedKeyword))
	}
	review := graph.ReviewInput{
		Level:     "trip",
		Dimension: "geo_scope",
		Score:     30,
		Passed:    false,
		Summary:   "目的地范围审核未通过，规划包含明显无关城市。",
		CriticalIssues: []string{
			"规划越界：" + strings.Join(issues, "、"),
		},
		ConstraintViolations: []graph.ConstraintViolation{
			{
				Dimension: "目的地范围",
				Rule:      "规划地点必须围绕用户指定目的地范围展开",
				Actual:    strings.Join(issues, "、"),
				Threshold: constraint.ScopeText,
				Severity:  "critical",
			},
		},
		Suggestions: []string{"请重新生成宏观阶段，删除不属于用户目的地范围的城市。"},
	}
	_, _ = a.graphClient.WriteReviewResult(ctx, tripPlanID, review)
	emitReviewAnnotation(ctx, trace, "overview", tripPlanID, "范围审核", "范围审核", "scope", review)
	return &TravelGeoScopeError{Violations: violations}
}
