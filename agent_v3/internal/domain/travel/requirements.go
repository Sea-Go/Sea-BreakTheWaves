package travel

// TravelRequirementSnapshot 是用户旅行需求的完整结构化快照。
// 它随着 intake（识别字段）和 merge（合并用户回答）逐步填充。
// 只有 RequirementReady=true 后才能进入正式规划。
type TravelRequirementSnapshot struct {
	DestinationScope string `json:"destination_scope"` // 目的地范围，如"全国""云南""大理"
	TotalDays        int    `json:"total_days"`        // 总天数
	StartDate        string `json:"start_date"`        // 出发日期 YYYY-MM-DD
	EndDate          string `json:"end_date"`          // 结束日期
	StartCity        string `json:"start_city"`        // 出发城市
	EndCity          string `json:"end_city"`          // 终点城市

	BudgetTotal            string   `json:"budget_total"`             // 总预算
	BudgetMonthly          string   `json:"budget_monthly"`           // 月预算
	TransportMode          string   `json:"transport_mode"`           // 交通方式：自驾/高铁/飞机/混合
	TravelStyle            []string `json:"travel_style"`             // 旅行风格：自然风光/历史文化/美食/慢旅行等
	Pace                   string   `json:"pace"`                     // 节奏：轻松/均衡/紧凑
	HighAltitudeAcceptance string   `json:"high_altitude_acceptance"` // 高海拔接受度
	DailyDrivingPreference string   `json:"daily_driving_preference"` // 日均驾驶强度偏好

	AccommodationStyle string                      `json:"accommodation_style"`           // 住宿偏好
	FoodPreference     []string                    `json:"food_preference"`               // 饮食偏好
	MustVisit          []string                    `json:"must_visit"`                    // 必去地点
	AvoidPlaces        []string                    `json:"avoid_places"`                  // 不想去的地点
	SpecialConstraints []string                    `json:"special_constraints"`           // 特殊限制
	DestinationAnchors []DestinationAnchorSnapshot `json:"destination_anchors,omitempty"` // 显式目的地与推导景观锚点

	MissingFields    []string `json:"missing_fields"`    // 当前缺失的字段
	RequirementReady bool     `json:"requirement_ready"` // true=可以进入正式规划
}

// DestinationAnchorSnapshot 是可公开展示/审核的目的地锚点。
// 用户明确目的地和系统推导的自然景观点分开标记，不把推导内容混进用户原话。
type DestinationAnchorSnapshot struct {
	Destination string   `json:"destination"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`   // destination | scenic | viewpoint | hiking | lake | mountain
	Origin      string   `json:"origin"` // user_explicit | system_inferred
	Priority    int      `json:"priority"`
	Themes      []string `json:"themes,omitempty"`
	Query       string   `json:"query,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	MustCover   bool     `json:"must_cover,omitempty"`
}
