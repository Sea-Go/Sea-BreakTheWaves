package travel

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	anchorOriginUserExplicit   = "user_explicit"
	anchorOriginSystemInferred = "system_inferred"
)

type destinationAnchorCatalogEntry struct {
	Destination string
	Aliases     []string
	Highland    bool
	Anchors     []DestinationAnchorSnapshot
}

var destinationAnchorCatalog = []destinationAnchorCatalogEntry{
	{
		Destination: "香格里拉",
		Aliases:     []string{"香格里拉", "迪庆"},
		Highland:    true,
		Anchors: []DestinationAnchorSnapshot{
			{Name: "梅里雪山", Kind: "mountain", Priority: 98, Themes: []string{"自然风光", "雪山", "日照金山"}, Query: "香格里拉 梅里雪山 自驾 攻略", Reason: "滇西北最具辨识度的雪山景观，适合自然风光偏好"},
			{Name: "普达措国家公园", Kind: "scenic", Priority: 90, Themes: []string{"自然风光", "湖泊", "森林"}, Query: "香格里拉 普达措 国家公园 自驾 攻略", Reason: "香格里拉核心自然景区，兼具湖泊、森林和高原草甸"},
			{Name: "纳帕海", Kind: "lake", Priority: 86, Themes: []string{"自然风光", "湿地", "草原"}, Query: "香格里拉 纳帕海 自驾 攻略", Reason: "高原湿地与草甸景观，适合自驾环湖"},
			{Name: "虎跳峡", Kind: "hiking", Priority: 82, Themes: []string{"自然风光", "峡谷", "徒步"}, Query: "香格里拉 虎跳峡 徒步 自驾 攻略", Reason: "峡谷徒步和金沙江景观，是丽江到香格里拉方向的强体验点"},
		},
	},
	{
		Destination: "稻城亚丁",
		Aliases:     []string{"稻城亚丁", "亚丁", "稻城"},
		Highland:    true,
		Anchors: []DestinationAnchorSnapshot{
			{Name: "洛绒牛场", Kind: "scenic", Priority: 96, Themes: []string{"自然风光", "雪山", "草甸"}, Query: "稻城亚丁 洛绒牛场 徒步 攻略", Reason: "稻城亚丁徒步核心节点，可近观三神山"},
			{Name: "牛奶海", Kind: "lake", Priority: 94, Themes: []string{"自然风光", "高山湖泊", "徒步"}, Query: "稻城亚丁 牛奶海 徒步 攻略", Reason: "亚丁长线徒步的代表性高山湖泊"},
			{Name: "五色海", Kind: "lake", Priority: 92, Themes: []string{"自然风光", "高山湖泊", "徒步"}, Query: "稻城亚丁 五色海 徒步 攻略", Reason: "亚丁长线徒步的核心湖泊景观"},
			{Name: "三神山", Kind: "mountain", Priority: 90, Themes: []string{"自然风光", "雪山"}, Query: "稻城亚丁 三神山 观景 攻略", Reason: "仙乃日、央迈勇、夏诺多吉构成稻城亚丁的核心雪山景观"},
		},
	},
	{
		Destination: "林芝",
		Aliases:     []string{"林芝", "鲁朗", "巴松措", "南迦巴瓦"},
		Highland:    true,
		Anchors: []DestinationAnchorSnapshot{
			{Name: "南迦巴瓦峰", Kind: "mountain", Priority: 99, Themes: []string{"自然风光", "雪山", "观景"}, Query: "林芝 南迦巴瓦 自驾 攻略", Reason: "林芝自然风光的标志性雪山，不能用市区到达替代"},
			{Name: "雅鲁藏布大峡谷", Kind: "scenic", Priority: 95, Themes: []string{"自然风光", "峡谷", "雪山"}, Query: "林芝 雅鲁藏布大峡谷 南迦巴瓦 攻略", Reason: "兼具峡谷、雪山和观景路线，是林芝核心自然体验"},
			{Name: "巴松措", Kind: "lake", Priority: 90, Themes: []string{"自然风光", "湖泊", "森林"}, Query: "林芝 巴松措 自驾 攻略", Reason: "林芝高山湖泊与森林景观代表"},
			{Name: "鲁朗林海", Kind: "scenic", Priority: 86, Themes: []string{"自然风光", "森林", "高原牧场"}, Query: "林芝 鲁朗林海 自驾 攻略", Reason: "林芝森林和牧场景观代表，适合自驾串联"},
			{Name: "色季拉山", Kind: "viewpoint", Priority: 82, Themes: []string{"自然风光", "观景", "雪山"}, Query: "林芝 色季拉山 南迦巴瓦 观景 攻略", Reason: "常见南迦巴瓦观景节点，可用于天气和观景窗口权衡"},
		},
	},
}

func enrichRequirementWithDeterministicFields(snap *TravelRequirementSnapshot, userMessage string) {
	if snap == nil {
		return
	}
	latest := latestUserTurnText(userMessage)
	if snap.StartCity == "" {
		snap.StartCity = parseStartCity(latest)
	}
	if snap.TotalDays == 0 {
		snap.TotalDays = parseTotalDays(latest)
	}
	if snap.BudgetTotal == "" {
		snap.BudgetTotal = parseBudgetTotal(latest)
	}
	if snap.TransportMode == "" && strings.Contains(latest, "自驾") {
		snap.TransportMode = "自驾"
	}
	if snap.Pace == "" {
		switch {
		case strings.Contains(latest, "轻松") || strings.Contains(latest, "慢"):
			snap.Pace = "轻松"
		case strings.Contains(latest, "紧凑") || strings.Contains(latest, "多打卡"):
			snap.Pace = "紧凑"
		case strings.Contains(latest, "均衡"):
			snap.Pace = "均衡"
		}
	}
	if containsAny(latest, []string{"自然风光", "自然", "风景", "雪山", "峡谷", "湖泊", "森林", "草原", "徒步"}) {
		snap.TravelStyle = appendUniqueStrings(snap.TravelStyle, "自然风光")
	}
	if snap.HighAltitudeAcceptance == "" {
		snap.HighAltitudeAcceptance = parseHighAltitudeAcceptance(latest)
	}
	if snap.DailyDrivingPreference == "" {
		snap.DailyDrivingPreference = parseDailyDrivingPreference(latest)
	}

	names := extractDestinationNames(strings.Join([]string{
		latest,
		snap.DestinationScope,
		strings.Join(snap.MustVisit, " "),
	}, " "))
	if snap.DestinationScope == "" && len(names) > 0 {
		snap.DestinationScope = strings.Join(names, "、")
	}
	snap.DestinationAnchors = deriveDestinationAnchors(*snap, latest)
}

func latestUserTurnText(userMessage string) string {
	userMessage = strings.TrimSpace(userMessage)
	if userMessage == "" {
		return ""
	}
	lines := strings.Split(userMessage, "\n")
	lastIdx := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "用户第") && strings.Contains(line, "：") {
			lastIdx = i
		}
	}
	if lastIdx >= 0 {
		line := strings.TrimSpace(lines[lastIdx])
		parts := strings.SplitN(line, "：", 2)
		if len(parts) == 2 {
			lines[lastIdx] = parts[1]
		}
		return strings.TrimSpace(strings.Join(lines[lastIdx:], "\n"))
	}
	return userMessage
}

func isLikelyNewPlanningRequest(userMessage string) bool {
	latest := latestUserTurnText(userMessage)
	if len([]rune(latest)) < 18 {
		return false
	}
	if parseTotalDays(latest) == 0 || parseStartCity(latest) == "" {
		return false
	}
	if len(extractDestinationNames(latest)) == 0 && !containsAny(latest, []string{"去", "目的地", "旅行", "旅游"}) {
		return false
	}
	return containsAny(latest, []string{"规划", "旅行", "旅游", "行程", "自驾", "出发"})
}

func parseStartCity(text string) string {
	re := regexp.MustCompile(`从\s*([\p{Han}A-Za-z]{2,12})\s*出发`)
	m := re.FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	return strings.Trim(m[1], "，,。；;、 ")
}

func parseTotalDays(text string) int {
	re := regexp.MustCompile(`(\d{1,4})\s*天`)
	m := re.FindStringSubmatch(text)
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func parseBudgetTotal(text string) string {
	re := regexp.MustCompile(`(\d+(?:\.\d+)?)\s*([wW万])`)
	m := re.FindStringSubmatch(text)
	if len(m) < 3 {
		return ""
	}
	unit := m[2]
	if unit == "w" || unit == "W" {
		unit = "万"
	}
	return m[1] + unit
}

func parseHighAltitudeAcceptance(text string) string {
	switch {
	case containsAny(text, []string{"不接受高海拔", "不能高海拔", "怕高反", "高反严重", "不去高海拔"}):
		return "不接受高海拔"
	case containsAny(text, []string{"接受高海拔", "能接受高海拔", "可以高海拔", "高反没问题", "能接受高反"}):
		return "可接受高海拔"
	default:
		return ""
	}
}

func parseDailyDrivingPreference(text string) string {
	switch {
	case containsAny(text, []string{"不接受长途", "不想长途", "少开车", "每天少开", "不想开太久"}):
		return "控制日均驾驶"
	case containsAny(text, []string{"接受长途", "能接受长途", "可以长途", "高强度自驾", "长途没问题"}) ||
		(containsAny(text, []string{"能接受", "可接受", "接受", "可以"}) && strings.Contains(text, "长途")):
		return "可接受较长日均驾驶"
	default:
		return ""
	}
}

func extractDestinationNames(text string) []string {
	seen := map[string]bool{}
	names := make([]string, 0)
	for _, entry := range destinationAnchorCatalog {
		for _, alias := range append([]string{entry.Destination}, entry.Aliases...) {
			if strings.Contains(text, alias) {
				if !seen[entry.Destination] {
					seen[entry.Destination] = true
					names = append(names, entry.Destination)
				}
				break
			}
		}
	}
	return names
}

func deriveDestinationAnchors(snap TravelRequirementSnapshot, rawUserMessage string) []DestinationAnchorSnapshot {
	text := strings.Join([]string{
		rawUserMessage,
		snap.DestinationScope,
		strings.Join(snap.MustVisit, " "),
	}, " ")
	destinations := extractDestinationNames(text)
	if len(destinations) == 0 {
		return nil
	}

	naturalTrip := isNaturalSceneryTrip(snap)
	out := make([]DestinationAnchorSnapshot, 0)
	for _, dest := range destinations {
		entry, ok := destinationCatalogEntry(dest)
		if !ok {
			continue
		}
		out = append(out, DestinationAnchorSnapshot{
			Destination: dest,
			Name:        dest,
			Kind:        "destination",
			Origin:      anchorOriginUserExplicit,
			Priority:    100,
			Themes:      []string{"目的地"},
			Query:       dest + " 自驾 旅行攻略",
			Reason:      "用户明确提到的目的地，规划必须覆盖到可验证地点或给出不可行原因",
			MustCover:   true,
		})
		if naturalTrip || entry.Highland {
			for _, anchor := range entry.Anchors {
				anchor.Destination = dest
				anchor.Origin = anchorOriginSystemInferred
				anchor.MustCover = false
				out = append(out, anchor)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Destination == out[j].Destination {
			return out[i].Priority > out[j].Priority
		}
		return out[i].Destination < out[j].Destination
	})
	return dedupeDestinationAnchors(out)
}

func destinationCatalogEntry(destination string) (destinationAnchorCatalogEntry, bool) {
	for _, entry := range destinationAnchorCatalog {
		if entry.Destination == destination {
			return entry, true
		}
	}
	return destinationAnchorCatalogEntry{}, false
}

func isNaturalSceneryTrip(snap TravelRequirementSnapshot) bool {
	for _, style := range snap.TravelStyle {
		if containsAny(style, []string{"自然", "风光", "风景", "摄影", "徒步"}) {
			return true
		}
	}
	for _, anchor := range snap.DestinationAnchors {
		if anchor.Kind != "destination" {
			return true
		}
	}
	return containsAny(snap.DestinationScope, []string{"稻城", "亚丁", "香格里拉", "林芝", "雪山", "峡谷", "湖"})
}

func requiresHighAltitudeCheck(snap TravelRequirementSnapshot) bool {
	if containsAny(snap.DestinationScope, []string{"稻城", "亚丁", "香格里拉", "林芝", "西藏", "川西", "高原", "雪山"}) {
		return true
	}
	for _, anchor := range snap.DestinationAnchors {
		if entry, ok := destinationCatalogEntry(anchor.Destination); ok && entry.Highland {
			return true
		}
	}
	return false
}

func requiresDrivingIntensityCheck(snap TravelRequirementSnapshot) bool {
	if !strings.Contains(snap.TransportMode, "自驾") {
		return false
	}
	explicitDestinations := 0
	for _, anchor := range snap.DestinationAnchors {
		if anchor.Origin == anchorOriginUserExplicit && anchor.MustCover {
			explicitDestinations++
		}
	}
	return explicitDestinations >= 2 || snap.TotalDays >= 5
}

func anchorSearchTermsFromText(text string) []DestinationAnchorSnapshot {
	var out []DestinationAnchorSnapshot
	for _, entry := range destinationAnchorCatalog {
		for _, anchor := range entry.Anchors {
			if strings.Contains(text, anchor.Name) {
				anchor.Destination = entry.Destination
				anchor.Origin = anchorOriginSystemInferred
				out = append(out, anchor)
			}
		}
	}
	if len(out) > 0 {
		return dedupeDestinationAnchors(out)
	}
	for _, entry := range destinationAnchorCatalog {
		if strings.Contains(text, entry.Destination) {
			out = append(out, entry.Anchors...)
		}
	}
	return dedupeDestinationAnchors(out)
}

func dedupeDestinationAnchors(values []DestinationAnchorSnapshot) []DestinationAnchorSnapshot {
	seen := map[string]bool{}
	out := make([]DestinationAnchorSnapshot, 0, len(values))
	for _, value := range values {
		value.Name = strings.TrimSpace(value.Name)
		value.Destination = strings.TrimSpace(value.Destination)
		if value.Name == "" {
			continue
		}
		key := value.Destination + "|" + value.Name + "|" + value.Origin
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

func appendUniqueStrings(values []string, more ...string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values)+len(more))
	for _, value := range append(values, more...) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func containsAny(text string, needles []string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

const (
	OriginUserExplicit   = anchorOriginUserExplicit
	OriginSystemInferred = anchorOriginSystemInferred
)

func EnrichRequirementWithDeterministicFields(snap *TravelRequirementSnapshot, userMessage string) {
	enrichRequirementWithDeterministicFields(snap, userMessage)
}

func LatestUserTurnText(userMessage string) string {
	return latestUserTurnText(userMessage)
}

func IsLikelyNewPlanningRequest(userMessage string) bool {
	return isLikelyNewPlanningRequest(userMessage)
}

func ParseStartCity(text string) string {
	return parseStartCity(text)
}

func ParseTotalDays(text string) int {
	return parseTotalDays(text)
}

func ParseBudgetTotal(text string) string {
	return parseBudgetTotal(text)
}

func ParseHighAltitudeAcceptance(text string) string {
	return parseHighAltitudeAcceptance(text)
}

func ParseDailyDrivingPreference(text string) string {
	return parseDailyDrivingPreference(text)
}

func DeriveDestinationAnchors(snap TravelRequirementSnapshot, rawUserMessage string) []DestinationAnchorSnapshot {
	return deriveDestinationAnchors(snap, rawUserMessage)
}

func RequiresHighAltitudeCheck(snap TravelRequirementSnapshot) bool {
	return requiresHighAltitudeCheck(snap)
}

func RequiresDrivingIntensityCheck(snap TravelRequirementSnapshot) bool {
	return requiresDrivingIntensityCheck(snap)
}

func AnchorSearchTermsFromText(text string) []DestinationAnchorSnapshot {
	return anchorSearchTermsFromText(text)
}

func DedupeDestinationAnchors(values []DestinationAnchorSnapshot) []DestinationAnchorSnapshot {
	return dedupeDestinationAnchors(values)
}

func AppendUniqueStrings(values []string, more ...string) []string {
	return appendUniqueStrings(values, more...)
}

func ContainsAny(text string, needles []string) bool {
	return containsAny(text, needles)
}
