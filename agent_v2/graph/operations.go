package graph

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// --- Write operations ---

// CreateTripPlan creates the root TripPlan node.
func (c *Client) CreateTripPlan(ctx context.Context, tp TripPlanNode) (string, error) {
	if tp.ID == "" {
		tp.ID = uuid.NewString()
	}
	if tp.Status == "" {
		tp.Status = StatusOutlined
	}
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherCreateTripPlan, map[string]any{
			"id": tp.ID, "name": tp.Name, "startDate": tp.StartDate, "endDate": tp.EndDate,
			"totalDays": tp.TotalDays, "budgetTotal": tp.BudgetTotal, "travelStyle": tp.TravelStyle,
			"transportMode": tp.TransportMode, "interests": tp.Interests, "mustVisit": tp.MustVisit,
			"avoid": tp.Avoid, "rawRequirements": tp.RawRequirements, "status": tp.Status,
			"maxConsecutiveHighIntensityDays": tp.MaxConsecutiveHighIntensityDays,
		})
		return nil, err
	})
	if err != nil {
		return "", fmt.Errorf("graph: create trip plan: %w", err)
	}
	return tp.ID, nil
}

// SplitChildInput describes a single child node to create during a split.
type SplitChildInput struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Seq       int            `json:"seq"`
	StartDate string         `json:"startDate,omitempty"`
	EndDate   string         `json:"endDate,omitempty"`
	Region    string         `json:"region,omitempty"`
	Props     map[string]any `json:"props,omitempty"`
}

// SplitParentNode splits a parent node into N child nodes.
func (c *Client) SplitParentNode(ctx context.Context, parentID, childType string, children []SplitChildInput) ([]string, error) {
	cypher := FormatSplitCypher(childType)
	childMaps := make([]map[string]any, len(children))
	for i, ch := range children {
		if ch.ID == "" {
			ch.ID = uuid.NewString()
		}
		childMaps[i] = map[string]any{
			"id": ch.ID, "name": ch.Name, "seq": ch.Seq,
			"startDate": ch.StartDate, "endDate": ch.EndDate,
			"region": ch.Region, "props": ch.Props,
		}
	}
	result, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		rec, err := tx.Run(ctx, cypher, map[string]any{
			"parentID": parentID,
			"children": childMaps,
		})
		if err != nil {
			return nil, err
		}
		if rec.Next(ctx) {
			ids, _ := rec.Record().Get("ids")
			return ids, nil
		}
		return nil, rec.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("graph: split parent: %w", err)
	}
	idList, _ := result.([]any)
	ids := make([]string, len(idList))
	for i, v := range idList {
		ids[i] = fmt.Sprint(v)
	}
	return ids, nil
}

// POIInput describes a POI to upsert into a Day.
type POIInput struct {
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

// UpsertPOIToDay creates or updates a POI and links it to a Day.
func (c *Client) UpsertPOIToDay(ctx context.Context, dayID string, poi POIInput) (string, error) {
	if poi.ID == "" {
		poi.ID = uuid.NewString()
	}
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherUpsertPOI, map[string]any{
			"poiID": poi.ID, "dayID": dayID,
			"name": poi.Name, "type": poi.Type, "lat": poi.Lat, "lng": poi.Lng,
			"address": poi.Address, "district": poi.District, "city": poi.City,
			"amapPOIID": poi.AmapPOIID, "visitOrder": poi.VisitOrder,
			"startTime": poi.StartTime, "endTime": poi.EndTime, "duration": poi.Duration,
			"isMainStop": poi.IsMainStop, "isOptional": poi.IsOptional,
			"isRainyDayBackup": poi.IsRainyDayBackup, "notes": poi.Notes,
			"verifiedBy": poi.VerifiedBy, "estimatedCost": poi.EstimatedCost,
		})
		return nil, err
	})
	if err != nil {
		return "", fmt.Errorf("graph: upsert poi: %w", err)
	}
	return poi.ID, nil
}

// RouteInput describes a route between two POIs.
type RouteInput struct {
	FromPOIID      string  `json:"fromPOIID"`
	ToPOIID        string  `json:"toPOIID"`
	TransportMode  string  `json:"transportMode"`
	DistanceMeters float64 `json:"distanceMeters"`
	DurationMin    float64 `json:"durationMin"`
	EstimatedCost  float64 `json:"estimatedCost"`
	Notes          string  `json:"notes"`
}

// WriteRoute creates a route relationship between two POIs.
func (c *Client) WriteRoute(ctx context.Context, route RouteInput) error {
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherWriteRoute, map[string]any{
			"fromPOIID": route.FromPOIID, "toPOIID": route.ToPOIID,
			"transportMode": route.TransportMode, "distanceMeters": route.DistanceMeters,
			"durationMin": route.DurationMin, "estimatedCost": route.EstimatedCost,
			"notes": route.Notes,
		})
		return nil, err
	})
	if err != nil {
		return fmt.Errorf("graph: write route: %w", err)
	}
	return nil
}

// GuideInsightInput describes a guide insight to write.
type GuideInsightInput struct {
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

// WriteGuideInsight writes a guide insight and links it to TripPlan and optionally to POIs/regions.
func (c *Client) WriteGuideInsight(ctx context.Context, tripPlanID string, insight GuideInsightInput) (string, error) {
	if insight.ID == "" {
		insight.ID = uuid.NewString()
	}
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherWriteGuideInsight, map[string]any{
			"id": insight.ID, "tripPlanID": tripPlanID,
			"source": insight.Source, "sourceTitle": insight.SourceTitle,
			"sourceURL": insight.SourceURL, "authorName": insight.AuthorName,
			"contentSummary": insight.ContentSummary, "keywords": insight.Keywords,
			"sentiment": insight.Sentiment, "matchedPOIs": insight.MatchedPOIs,
			"matchedRegion": insight.MatchedRegion,
		})
		return nil, err
	})
	if err != nil {
		return "", fmt.Errorf("graph: write guide insight: %w", err)
	}
	return insight.ID, nil
}

// LinkInsightToPOI links a guide insight to a specific POI.
func (c *Client) LinkInsightToPOI(ctx context.Context, insightID, poiID string) error {
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherLinkInsightToPOI, map[string]any{
			"insightID": insightID,
			"poiID":     poiID,
		})
		return nil, err
	})
	return err
}

// LinkInsightToRegion links a guide insight to a Phase region.
func (c *Client) LinkInsightToRegion(ctx context.Context, insightID, phaseID string) error {
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherLinkInsightToRegion, map[string]any{
			"insightID": insightID,
			"phaseID":   phaseID,
		})
		return nil, err
	})
	return err
}

// ReviewInput describes a review result to write.
type ReviewInput struct {
	ID                   string                `json:"id"`
	Level                string                `json:"level"`
	Dimension            string                `json:"dimension"`
	Score                int                   `json:"score"`
	Passed               bool                  `json:"passed"`
	CriticalIssues       []string              `json:"criticalIssues"`
	Issues               []string              `json:"issues"`
	Suggestions          []string              `json:"suggestions"`
	Summary              string                `json:"summary"`
	ConstraintViolations []ConstraintViolation `json:"constraintViolations"`
}

// WriteReviewResult writes a review result and links it to the target node.
func (c *Client) WriteReviewResult(ctx context.Context, targetNodeID string, review ReviewInput) (string, error) {
	if review.ID == "" {
		review.ID = uuid.NewString()
	}
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherWriteReviewResult, map[string]any{
			"id": review.ID, "targetNodeID": targetNodeID,
			"level": review.Level, "dimension": review.Dimension,
			"score": review.Score, "passed": review.Passed,
			"criticalIssues": review.CriticalIssues, "issues": review.Issues,
			"suggestions": review.Suggestions, "summary": review.Summary,
		})
		return nil, err
	})
	if err != nil {
		return "", fmt.Errorf("graph: write review: %w", err)
	}
	return review.ID, nil
}

// UpdateNode updates one or more properties on any node.
func (c *Client) UpdateNode(ctx context.Context, nodeID string, props map[string]any) error {
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherUpdateNode, map[string]any{
			"nodeID":     nodeID,
			"properties": props,
		})
		return nil, err
	})
	if err != nil {
		return fmt.Errorf("graph: update node: %w", err)
	}
	return nil
}

// WriteClimateData writes climate data for a region+month combination.
func (c *Client) WriteClimateData(ctx context.Context, cd ClimateDataNode) error {
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherWriteClimateData, map[string]any{
			"region": cd.Region, "month": cd.Month,
			"avgHighTemp": cd.AvgHighTemp, "avgLowTemp": cd.AvgLowTemp,
			"precipitation": cd.Precipitation, "humidity": cd.Humidity,
			"rainyDays": cd.RainyDays, "sunriseTime": cd.SunriseTime,
			"sunsetTime": cd.SunsetTime, "extremeWeatherRisk": cd.ExtremeWeatherRisk,
		})
		return nil, err
	})
	return err
}

// --- Read operations ---

// DaySubgraph contains the full context for a single Day.
type DaySubgraph struct {
	Day      DayNode            `json:"day"`
	POIs     []POINode          `json:"pois"`
	Routes   []map[string]any   `json:"routes"`
	Insights []GuideInsightNode `json:"insights"`
	Reviews  []ReviewResultNode `json:"reviews"`
	Climate  []ClimateDataNode  `json:"climate"`
}

// GetDaySubgraph returns the full subgraph for a single Day node.
func (c *Client) GetDaySubgraph(ctx context.Context, dayID string) (*DaySubgraph, error) {
	result, err := c.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		rec, err := tx.Run(ctx, cypherGetDaySubgraph, map[string]any{"nodeID": dayID})
		if err != nil {
			return nil, err
		}
		if rec.Next(ctx) {
			return rec.Record().AsMap(), nil
		}
		return nil, rec.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("graph: get day subgraph: %w", err)
	}
	return mapToDaySubgraph(result.(map[string]any)), nil
}

func mapToDaySubgraph(m map[string]any) *DaySubgraph {
	sg := &DaySubgraph{}
	if day, ok := m["day"].(neo4j.Node); ok {
		sg.Day = nodeToDayNode(day)
	}
	if pois, ok := m["pois"].([]any); ok {
		for _, p := range pois {
			if n, ok := p.(neo4j.Node); ok {
				sg.POIs = append(sg.POIs, nodeToPOINode(n))
			}
		}
	}
	if routes, ok := m["routes"].([]any); ok {
		for _, r := range routes {
			if rm, ok := r.(map[string]any); ok {
				sg.Routes = append(sg.Routes, rm)
			}
		}
	}
	if insights, ok := m["insights"].([]any); ok {
		for _, i := range insights {
			if n, ok := i.(neo4j.Node); ok {
				sg.Insights = append(sg.Insights, nodeToGuideInsightNode(n))
			}
		}
	}
	if reviews, ok := m["reviews"].([]any); ok {
		for _, r := range reviews {
			if n, ok := r.(neo4j.Node); ok {
				sg.Reviews = append(sg.Reviews, nodeToReviewResultNode(n))
			}
		}
	}
	if climate, ok := m["climate"].([]any); ok {
		for _, c := range climate {
			if n, ok := c.(neo4j.Node); ok {
				sg.Climate = append(sg.Climate, nodeToClimateDataNode(n))
			}
		}
	}
	return sg
}

// ChildrenSummary returns direct children of a node.
func (c *Client) ChildrenSummary(ctx context.Context, parentID string) ([]map[string]any, error) {
	result, err := c.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		rec, err := tx.Run(ctx, cypherGetChildren, map[string]any{"parentID": parentID})
		if err != nil {
			return nil, err
		}
		if rec.Next(ctx) {
			val, _ := rec.Record().Get("children")
			return val, nil
		}
		return nil, rec.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("graph: get children: %w", err)
	}
	children, _ := result.([]any)
	out := make([]map[string]any, len(children))
	for i, c := range children {
		out[i], _ = c.(map[string]any)
	}
	return out, nil
}

// TripOverview contains the full hierarchy tree summary.
type TripOverview struct {
	TripPlan TripPlanNode     `json:"tripPlan"`
	Phases   []map[string]any `json:"phases"`
	Months   []map[string]any `json:"months"`
	Weeks    []map[string]any `json:"weeks"`
	Days     []map[string]any `json:"days"`
}

// GetTripOverview returns the full hierarchy tree summary.
func (c *Client) GetTripOverview(ctx context.Context, tripPlanID string) (*TripOverview, error) {
	result, err := c.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		rec, err := tx.Run(ctx, cypherGetTripOverview, map[string]any{"tripPlanID": tripPlanID})
		if err != nil {
			return nil, err
		}
		if rec.Next(ctx) {
			return rec.Record().AsMap(), nil
		}
		return nil, rec.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("graph: get trip overview: %w", err)
	}
	overview := &TripOverview{}
	m := result.(map[string]any)
	if tp, ok := m["tp"].(neo4j.Node); ok {
		overview.TripPlan = nodeToTripPlanNode(tp)
	}
	overview.Phases = toMapSlice(m["phases"])
	overview.Months = toMapSlice(m["months"])
	overview.Weeks = toMapSlice(m["weeks"])
	overview.Days = toMapSlice(m["days"])
	return overview, nil
}

// WeatherContext bundles climate, constraint, and seasonal event data.
type WeatherContext struct {
	ClimateData    []ClimateDataNode       `json:"climateData"`
	Constraints    []WeatherConstraintNode `json:"constraints"`
	SeasonalEvents []SeasonalEventNode     `json:"seasonalEvents"`
}

// GetWeatherContext returns climate + constraint + seasonal data for a region+month.
func (c *Client) GetWeatherContext(ctx context.Context, region string, month int) (*WeatherContext, error) {
	result, err := c.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		rec, err := tx.Run(ctx, cypherGetWeatherContext, map[string]any{
			"region": region,
			"month":  month,
		})
		if err != nil {
			return nil, err
		}
		if rec.Next(ctx) {
			return rec.Record().AsMap(), nil
		}
		return nil, rec.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("graph: get weather context: %w", err)
	}
	wc := &WeatherContext{}
	m := result.(map[string]any)
	if cdList, ok := m["climateData"].([]any); ok {
		for _, cd := range cdList {
			if n, ok := cd.(neo4j.Node); ok {
				wc.ClimateData = append(wc.ClimateData, nodeToClimateDataNode(n))
			}
		}
	}
	if wcList, ok := m["constraints"].([]any); ok {
		for _, w := range wcList {
			if n, ok := w.(neo4j.Node); ok {
				wc.Constraints = append(wc.Constraints, nodeToWeatherConstraintNode(n))
			}
		}
	}
	if seList, ok := m["seasonalEvents"].([]any); ok {
		for _, se := range seList {
			if n, ok := se.(neo4j.Node); ok {
				wc.SeasonalEvents = append(wc.SeasonalEvents, nodeToSeasonalEventNode(n))
			}
		}
	}
	return wc, nil
}

// GetUnplannedNodes returns children of parent that are not yet done/reviewed.
func (c *Client) GetUnplannedNodes(ctx context.Context, parentID string) ([]map[string]any, error) {
	result, err := c.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		rec, err := tx.Run(ctx, cypherGetUnplannedNodes, map[string]any{"parentID": parentID})
		if err != nil {
			return nil, err
		}
		if rec.Next(ctx) {
			val, _ := rec.Record().Get("unplanned")
			return val, nil
		}
		return nil, rec.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("graph: get unplanned: %w", err)
	}
	return toMapSlice(result), nil
}

// GetLayerReviewStatus returns review status for all children of a parent.
func (c *Client) GetLayerReviewStatus(ctx context.Context, parentID string) ([]map[string]any, error) {
	result, err := c.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		rec, err := tx.Run(ctx, cypherGetLayerReviewStatus, map[string]any{"parentID": parentID})
		if err != nil {
			return nil, err
		}
		if rec.Next(ctx) {
			val, _ := rec.Record().Get("reviewStatus")
			return val, nil
		}
		return nil, rec.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("graph: get layer review: %w", err)
	}
	return toMapSlice(result), nil
}

// GetConstraintViolations returns all failed reviews under a node.
func (c *Client) GetConstraintViolations(ctx context.Context, nodeID string) ([]map[string]any, error) {
	result, err := c.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		rec, err := tx.Run(ctx, cypherGetConstraintViolations, map[string]any{"nodeID": nodeID})
		if err != nil {
			return nil, err
		}
		if rec.Next(ctx) {
			val, _ := rec.Record().Get("violations")
			return val, nil
		}
		return nil, rec.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("graph: get violations: %w", err)
	}
	return toMapSlice(result), nil
}

// GetNodeBudgetSummary returns total cost under a node.
func (c *Client) GetNodeBudgetSummary(ctx context.Context, nodeID string) (float64, error) {
	result, err := c.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		rec, err := tx.Run(ctx, cypherGetNodeBudgetSummary, map[string]any{"nodeID": nodeID})
		if err != nil {
			return nil, err
		}
		if rec.Next(ctx) {
			val, _ := rec.Record().Get("totalCost")
			return val, nil
		}
		return nil, rec.Err()
	})
	if err != nil {
		return 0, fmt.Errorf("graph: get budget: %w", err)
	}
	if v, ok := result.(float64); ok {
		return v, nil
	}
	return 0, nil
}

// --- Helpers ---

func toMapSlice(v any) []map[string]any {
	list, _ := v.([]any)
	out := make([]map[string]any, len(list))
	for i, item := range list {
		out[i], _ = item.(map[string]any)
	}
	return out
}

func getStringProp(props map[string]any, key string) string {
	if v, ok := props[key].(string); ok {
		return v
	}
	return ""
}

func getFloatProp(props map[string]any, key string) float64 {
	switch v := props[key].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	default:
		return 0
	}
}

func getIntProp(props map[string]any, key string) int {
	switch v := props[key].(type) {
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func getBoolProp(props map[string]any, key string) bool {
	if v, ok := props[key].(bool); ok {
		return v
	}
	return false
}

func getStringSlice(props map[string]any, key string) []string {
	if v, ok := props[key].([]any); ok {
		out := make([]string, len(v))
		for i, item := range v {
			out[i] = fmt.Sprint(item)
		}
		return out
	}
	return nil
}

func nodeToTripPlanNode(n neo4j.Node) TripPlanNode {
	p := n.Props
	return TripPlanNode{
		ID: n.ElementId, Name: getStringProp(p, "name"),
		StartDate: getStringProp(p, "startDate"), EndDate: getStringProp(p, "endDate"),
		TotalDays: getIntProp(p, "totalDays"), BudgetTotal: getFloatProp(p, "budgetTotal"),
		TravelStyle: getStringProp(p, "travelStyle"), TransportMode: getStringProp(p, "transportMode"),
		Interests: getStringSlice(p, "interests"), MustVisit: getStringSlice(p, "mustVisit"),
		Avoid: getStringSlice(p, "avoid"), RawRequirements: getStringProp(p, "rawRequirements"),
		Status: getStringProp(p, "status"),
		MaxConsecutiveHighIntensityDays: getIntProp(p, "maxConsecutiveHighIntensityDays"),
	}
}

func nodeToDayNode(n neo4j.Node) DayNode {
	p := n.Props
	return DayNode{
		ID: n.ElementId, Date: getStringProp(p, "date"),
		DayIndex: getIntProp(p, "dayIndex"), Theme: getStringProp(p, "theme"),
		StartPoint: getStringProp(p, "startPoint"), PrimaryArea: getStringProp(p, "primaryArea"),
		RouteOverview: getStringProp(p, "routeOverview"), Intensity: getStringProp(p, "intensity"),
		ThinkingNotes: getStringProp(p, "thinkingNotes"), Status: getStringProp(p, "status"),
	}
}

func nodeToPOINode(n neo4j.Node) POINode {
	p := n.Props
	return POINode{
		ID: n.ElementId, Name: getStringProp(p, "name"),
		AmapPOIID: getStringProp(p, "amapPOIID"), Type: getStringProp(p, "type"),
		Lat: getFloatProp(p, "lat"), Lng: getFloatProp(p, "lng"),
		Address: getStringProp(p, "address"), District: getStringProp(p, "district"),
		City: getStringProp(p, "city"), VisitOrder: getIntProp(p, "visitOrder"),
		StartTime: getStringProp(p, "startTime"), EndTime: getStringProp(p, "endTime"),
		Duration: getIntProp(p, "duration"), IsMainStop: getBoolProp(p, "isMainStop"),
		IsOptional: getBoolProp(p, "isOptional"), IsRainyDayBackup: getBoolProp(p, "isRainyDayBackup"),
		Notes: getStringProp(p, "notes"), VerifiedBy: getStringProp(p, "verifiedBy"),
		EstimatedCost: getFloatProp(p, "estimatedCost"),
	}
}

func nodeToGuideInsightNode(n neo4j.Node) GuideInsightNode {
	p := n.Props
	return GuideInsightNode{
		ID: n.ElementId, Source: getStringProp(p, "source"),
		SourceTitle: getStringProp(p, "sourceTitle"), SourceURL: getStringProp(p, "sourceURL"),
		AuthorName: getStringProp(p, "authorName"), ContentSummary: getStringProp(p, "contentSummary"),
		Keywords: getStringSlice(p, "keywords"), Sentiment: getStringProp(p, "sentiment"),
		MatchedPOIs: getStringSlice(p, "matchedPOIs"), MatchedRegion: getStringProp(p, "matchedRegion"),
	}
}

func nodeToReviewResultNode(n neo4j.Node) ReviewResultNode {
	p := n.Props
	return ReviewResultNode{
		ID: n.ElementId, Level: getStringProp(p, "level"),
		Dimension: getStringProp(p, "dimension"), Score: getIntProp(p, "score"),
		Passed: getBoolProp(p, "passed"), CriticalIssues: getStringSlice(p, "criticalIssues"),
		Issues: getStringSlice(p, "issues"), Suggestions: getStringSlice(p, "suggestions"),
		Summary: getStringProp(p, "summary"),
	}
}

func nodeToClimateDataNode(n neo4j.Node) ClimateDataNode {
	p := n.Props
	return ClimateDataNode{
		ID: n.ElementId, Region: getStringProp(p, "region"),
		Month: getIntProp(p, "month"), AvgHighTemp: getFloatProp(p, "avgHighTemp"),
		AvgLowTemp: getFloatProp(p, "avgLowTemp"), Precipitation: getFloatProp(p, "precipitation"),
		Humidity: getFloatProp(p, "humidity"), RainyDays: getIntProp(p, "rainyDays"),
		SunriseTime: getStringProp(p, "sunriseTime"), SunsetTime: getStringProp(p, "sunsetTime"),
		ExtremeWeatherRisk: getStringProp(p, "extremeWeatherRisk"),
	}
}

func nodeToWeatherConstraintNode(n neo4j.Node) WeatherConstraintNode {
	p := n.Props
	return WeatherConstraintNode{
		ID: n.ElementId, Region: getStringProp(p, "region"),
		Month: getIntProp(p, "month"), ConstraintType: getStringProp(p, "constraintType"),
		Severity: getStringProp(p, "severity"),
		AffectedActivities: getStringSlice(p, "affectedActivities"),
		Threshold: getStringProp(p, "threshold"), Description: getStringProp(p, "description"),
	}
}

func nodeToSeasonalEventNode(n neo4j.Node) SeasonalEventNode {
	p := n.Props
	return SeasonalEventNode{
		ID: n.ElementId, Name: getStringProp(p, "name"),
		Region: getStringProp(p, "region"), StartMonth: getIntProp(p, "startMonth"),
		EndMonth: getIntProp(p, "endMonth"), Type: getStringProp(p, "type"),
		Description: getStringProp(p, "description"),
	}
}