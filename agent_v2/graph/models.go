package graph

// Node type constants.
const (
	NodeTypeTripPlan       = "TripPlan"
	NodeTypePhase          = "Phase"
	NodeTypeMonth          = "Month"
	NodeTypeWeek           = "Week"
	NodeTypeDay            = "Day"
	NodeTypePOI            = "POI"
	NodeTypeRouteSegment   = "RouteSegment"
	NodeTypeArea           = "Area"
	NodeTypeGuideInsight   = "GuideInsight"
	NodeTypeReviewResult   = "ReviewResult"
	NodeTypeClimateData    = "ClimateData"
	NodeTypeSeasonalEvent  = "SeasonalEvent"
	NodeTypeWeatherConstraint = "WeatherConstraint"
)

// Relationship type constants.
const (
	RelHasPhase        = "HAS_PHASE"
	RelHasMonth        = "HAS_MONTH"
	RelHasWeek         = "HAS_WEEK"
	RelHasDay          = "HAS_DAY"
	RelNextPhase       = "NEXT_PHASE"
	RelNextMonth       = "NEXT_MONTH"
	RelNextWeek        = "NEXT_WEEK"
	RelNextDay         = "NEXT_DAY"
	RelVisitsPOI       = "VISITS_POI"
	RelRoutesTo        = "ROUTES_TO"
	RelLocatedIn       = "LOCATED_IN"
	RelHasClimate      = "HAS_CLIMATE"
	RelExpectedWeather = "EXPECTED_WEATHER"
	RelAffectedBy      = "AFFECTED_BY"
	RelHasSeasonalEvent = "HAS_SEASONAL_EVENT"
	RelHasBackup       = "HAS_BACKUP"
	RelInsightForRegion = "INSIGHT_FOR_REGION"
	RelInsightForPOI   = "INSIGHT_FOR_POI"
	RelInsightBelongsTo = "INSIGHT_BELONGS_TO"
	RelReviewedBy      = "REVIEWED_BY"
	RelBelongsTo       = "BELONGS_TO"
)

// Status constants.
const (
	StatusOutlined   = "outlined"
	StatusPlanning   = "planning"
	StatusDecomposed = "decomposed"
	StatusVerified   = "verified"
	StatusReviewed   = "reviewed"
	StatusDone       = "done"
)

// TripPlanNode is the root for an entire trip planning session.
type TripPlanNode struct {
	ID                              string   `json:"id"`
	Name                            string   `json:"name"`
	StartDate                       string   `json:"startDate"`
	EndDate                         string   `json:"endDate"`
	TotalDays                       int      `json:"totalDays"`
	BudgetTotal                     float64  `json:"budgetTotal"`
	TravelStyle                     string   `json:"travelStyle"`
	TransportMode                   string   `json:"transportMode"`
	Interests                       []string `json:"interests"`
	MustVisit                       []string `json:"mustVisit"`
	Avoid                           []string `json:"avoid"`
	RawRequirements                 string   `json:"rawRequirements"`
	Status                          string   `json:"status"`
	MaxConsecutiveHighIntensityDays int      `json:"maxConsecutiveHighIntensityDays"`
	UserID                          string   `json:"userId"`
	SessionID                       string   `json:"sessionId"`
	RequestID                       string   `json:"requestId"`
}

// PhaseNode represents a seasonal/geographic phase (1-6 phases per year).
type PhaseNode struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Seq            int      `json:"seq"`
	StartDate      string   `json:"startDate"`
	EndDate        string   `json:"endDate"`
	Region         string   `json:"region"`
	Season         string   `json:"season"`
	Theme          string   `json:"theme"`
	ClimateSummary string   `json:"climateSummary"`
	EstimatedBudget float64 `json:"estimatedBudget"`
	Status         string   `json:"status"`
	DayCount       int      `json:"dayCount"`
}

// MonthNode represents one calendar month within a Phase.
type MonthNode struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	YearMonth    string  `json:"yearMonth"`
	Seq          int     `json:"seq"`
	Region       string  `json:"region"`
	PrimaryCity  string  `json:"primaryCity"`
	WeekCount    int     `json:"weekCount"`
	MonthlyBudget float64 `json:"monthlyBudget"`
	Status       string  `json:"status"`
}

// WeekNode represents one week within a Month.
type WeekNode struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Seq                   int    `json:"seq"`
	StartDate             string `json:"startDate"`
	EndDate               string `json:"endDate"`
	Region                string `json:"region"`
	PrimaryLocation       string `json:"primaryLocation"`
	Theme                 string `json:"theme"`
	RestDayCount          int    `json:"restDayCount"`
	TransferDayCount      int    `json:"transferDayCount"`
	HighIntensityDayCount int    `json:"highIntensityDayCount"`
	Status                string `json:"status"`
}

// DayNode represents a single travel day.
type DayNode struct {
	ID            string `json:"id"`
	Date          string `json:"date"`
	DayIndex      int    `json:"dayIndex"`
	Theme         string `json:"theme"`
	StartPoint    string `json:"startPoint"`
	PrimaryArea   string `json:"primaryArea"`
	RouteOverview string `json:"routeOverview"`
	Intensity     string `json:"intensity"`
	ThinkingNotes string `json:"thinkingNotes"`
	Status        string `json:"status"`
}

// POINode represents a point of interest.
type POINode struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	AmapPOIID        string  `json:"amapPOIID"`
	Type             string  `json:"type"`
	Lat              float64 `json:"lat"`
	Lng              float64 `json:"lng"`
	Address          string  `json:"address"`
	District         string  `json:"district"`
	City             string  `json:"city"`
	VisitOrder       int     `json:"visitOrder"`
	StartTime        string  `json:"startTime"`
	EndTime          string  `json:"endTime"`
	Duration         int     `json:"duration"`
	IsMainStop       bool    `json:"isMainStop"`
	IsOptional       bool    `json:"isOptional"`
	IsRainyDayBackup bool    `json:"isRainyDayBackup"`
	Notes            string  `json:"notes"`
	VerifiedBy       string  `json:"verifiedBy"`
	EstimatedCost    float64 `json:"estimatedCost"`
}

// RouteSegmentNode represents a connection between two POIs.
type RouteSegmentNode struct {
	ID             string  `json:"id"`
	FromPOIID      string  `json:"fromPOIID"`
	ToPOIID        string  `json:"toPOIID"`
	TransportMode  string  `json:"transportMode"`
	DistanceMeters float64 `json:"distanceMeters"`
	DurationMin    float64 `json:"durationMin"`
	Polyline       string  `json:"polyline"`
	EstimatedCost  float64 `json:"estimatedCost"`
	Notes          string  `json:"notes"`
}

// AreaNode represents a geographic cluster within a city/region.
type AreaNode struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	CenterLat   float64 `json:"centerLat"`
	CenterLng   float64 `json:"centerLng"`
	District    string  `json:"district"`
	City        string  `json:"city"`
	Description string  `json:"description"`
	POICount    int     `json:"poiCount"`
}

// GuideInsightNode represents extracted guide material from Zhihu or Bilibili.
type GuideInsightNode struct {
	ID             string   `json:"id"`
	Source         string   `json:"source"`
	SourceTitle    string   `json:"sourceTitle"`
	SourceURL      string   `json:"sourceURL"`
	AuthorName     string   `json:"authorName"`
	ContentSummary string   `json:"contentSummary"`
	Keywords       []string `json:"keywords"`
	Sentiment      string   `json:"sentiment"`
	MatchedPOIs    []string `json:"matchedPOIs"`
	MatchedRegion  string   `json:"matchedRegion"`
}

// ReviewResultNode is attached to plan nodes at any level.
type ReviewResultNode struct {
	ID                  string              `json:"id"`
	Level               string              `json:"level"`
	Dimension           string              `json:"dimension"`
	Score               int                 `json:"score"`
	Passed              bool                `json:"passed"`
	CriticalIssues      []string            `json:"criticalIssues"`
	Issues              []string            `json:"issues"`
	Suggestions         []string            `json:"suggestions"`
	Summary             string              `json:"summary"`
	ConstraintViolations []ConstraintViolation `json:"constraintViolations"`
}

// ConstraintViolation records a specific hard-constraint breach.
type ConstraintViolation struct {
	Dimension string  `json:"dimension"`
	Rule      string  `json:"rule"`
	Actual    string  `json:"actual"`
	Threshold string  `json:"threshold"`
	Severity  string  `json:"severity"`
}

// ClimateDataNode holds monthly historical climate averages for a region.
type ClimateDataNode struct {
	ID                string  `json:"id"`
	Region            string  `json:"region"`
	Month             int     `json:"month"`
	AvgHighTemp       float64 `json:"avgHighTemp"`
	AvgLowTemp        float64 `json:"avgLowTemp"`
	Precipitation     float64 `json:"precipitation"`
	Humidity          float64 `json:"humidity"`
	RainyDays         int     `json:"rainyDays"`
	SunriseTime       string  `json:"sunriseTime"`
	SunsetTime        string  `json:"sunsetTime"`
	ExtremeWeatherRisk string `json:"extremeWeatherRisk"`
}

// SeasonalEventNode represents seasonal natural/cultural events.
type SeasonalEventNode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Region      string `json:"region"`
	StartMonth  int    `json:"startMonth"`
	EndMonth    int    `json:"endMonth"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

// WeatherConstraintNode represents weather-based travel constraints.
type WeatherConstraintNode struct {
	ID                 string `json:"id"`
	Region             string `json:"region"`
	Month              int    `json:"month"`
	ConstraintType     string `json:"constraintType"`
	Severity           string `json:"severity"`
	AffectedActivities []string `json:"affectedActivities"`
	Threshold          string `json:"threshold"`
	Description        string `json:"description"`
}

// OutputChunkNode represents a paginated output chunk for cursor-based reading.
type OutputChunkNode struct {
	ID            string `json:"id"`
	TripPlanID    string `json:"tripPlanId"`
	NodeID        string `json:"nodeId"`
	Level         string `json:"level"`
	Seq           int    `json:"seq"`
	Title         string `json:"title"`
	Content       string `json:"content"`
	TokenEstimate int    `json:"tokenEstimate"`
	Status        string `json:"status"`
	CreatedAt     string `json:"createdAt"`
}