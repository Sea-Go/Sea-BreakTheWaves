package graph

import "fmt"

// --- Node creation ---

const cypherCreateTripPlan = `
CREATE (tp:TripPlan {
    id: $id, name: $name, startDate: $startDate, endDate: $endDate,
    totalDays: $totalDays, budgetTotal: $budgetTotal, travelStyle: $travelStyle,
    transportMode: $transportMode, interests: $interests, mustVisit: $mustVisit,
    avoid: $avoid, rawRequirements: $rawRequirements, status: $status,
    maxConsecutiveHighIntensityDays: $maxConsecutiveHighIntensityDays,
    userId: $userId, sessionId: $sessionId, requestId: $requestId
})
RETURN tp.id AS id
`

// --- Generic split ---

const cypherSplitParent = `
MATCH (parent {id: $parentID})
SET parent.status = 'decomposed'
WITH parent
UNWIND $children AS child
CREATE (parent)-[:%s]->(c:%s {
    id: child.id, name: child.name, seq: child.seq,
    startDate: child.startDate, endDate: child.endDate,
    region: child.region, status: 'outlined'
})
SET c += child.props
RETURN collect(c.id) AS ids
`

// --- POI ---

const cypherUpsertPOI = `
MERGE (poi:POI {id: $poiID})
SET poi.name = $name, poi.type = $type, poi.lat = $lat, poi.lng = $lng,
    poi.address = $address, poi.district = $district, poi.city = $city,
    poi.amapPOIID = $amapPOIID, poi.visitOrder = $visitOrder,
    poi.startTime = $startTime, poi.endTime = $endTime, poi.duration = $duration,
    poi.isMainStop = $isMainStop, poi.isOptional = $isOptional,
    poi.isRainyDayBackup = $isRainyDayBackup, poi.notes = $notes,
    poi.verifiedBy = $verifiedBy, poi.estimatedCost = $estimatedCost
WITH poi
MATCH (day:Day {id: $dayID})
MERGE (day)-[:VISITS_POI {visitOrder: $visitOrder}]->(poi)
RETURN poi.id AS poiID
`

// --- Route ---

const cypherWriteRoute = `
MATCH (from:POI {id: $fromPOIID}), (to:POI {id: $toPOIID})
MERGE (from)-[r:ROUTES_TO]->(to)
SET r.transportMode = $transportMode, r.distanceMeters = $distanceMeters,
    r.durationMin = $durationMin, r.estimatedCost = $estimatedCost, r.notes = $notes,
    r.fromPOIID = $fromPOIID, r.toPOIID = $toPOIID
RETURN true AS ok
`

// --- Guide insight ---

const cypherWriteGuideInsight = `
CREATE (gi:GuideInsight {
    id: $id, source: $source, sourceTitle: $sourceTitle, sourceURL: $sourceURL,
    authorName: $authorName, contentSummary: $contentSummary,
    keywords: $keywords, sentiment: $sentiment,
    matchedPOIs: $matchedPOIs, matchedRegion: $matchedRegion
})
WITH gi
MATCH (tp:TripPlan {id: $tripPlanID})
CREATE (gi)-[:INSIGHT_BELONGS_TO]->(tp)
RETURN gi.id AS insightID
`

const cypherLinkInsightToPOI = `
MATCH (gi:GuideInsight {id: $insightID}), (poi:POI {id: $poiID})
MERGE (gi)-[:INSIGHT_FOR_POI]->(poi)
`

const cypherLinkInsightToRegion = `
MATCH (gi:GuideInsight {id: $insightID}), (phase:Phase {id: $phaseID})
MERGE (gi)-[:INSIGHT_FOR_REGION]->(phase)
`

// --- Review ---

const cypherWriteReviewResult = `
MATCH (target {id: $targetNodeID})
CREATE (rr:ReviewResult {
    id: $id, level: $level, dimension: $dimension, score: $score,
    passed: $passed, criticalIssues: $criticalIssues,
    issues: $issues, suggestions: $suggestions, summary: $summary,
    constraintViolations: $constraintViolations
})
CREATE (target)-[:REVIEWED_BY]->(rr)
RETURN rr.id AS reviewID
`

// --- Generic update ---

const cypherUpdateNode = `
MATCH (n {id: $nodeID})
SET n += $properties
RETURN true AS ok
`

// --- Climate ---

const cypherWriteClimateData = `
MERGE (cd:ClimateData {region: $region, month: $month})
SET cd.avgHighTemp = $avgHighTemp, cd.avgLowTemp = $avgLowTemp,
    cd.precipitation = $precipitation, cd.humidity = $humidity,
    cd.rainyDays = $rainyDays, cd.sunriseTime = $sunriseTime,
    cd.sunsetTime = $sunsetTime, cd.extremeWeatherRisk = $extremeWeatherRisk
RETURN cd.region + '-' + toString(cd.month) AS id
`

// --- Read: subgraph ---

const cypherGetDaySubgraph = `
MATCH (day:Day {id: $nodeID})
OPTIONAL MATCH (day)-[:VISITS_POI]->(poi:POI)
OPTIONAL MATCH (poi)-[rt:ROUTES_TO]->(toPOI:POI)
OPTIONAL MATCH (gi:GuideInsight)-[:INSIGHT_FOR_POI]->(poi)
OPTIONAL MATCH (day)-[:REVIEWED_BY]->(rr:ReviewResult)
OPTIONAL MATCH (day)-[:EXPECTED_WEATHER]->(cd:ClimateData)
RETURN day,
       collect(DISTINCT poi) AS pois,
       collect(DISTINCT {fromPOIID: rt.fromPOIID, toPOIID: rt.toPOIID,
               fromName: poi.name, toName: toPOI.name,
               mode: rt.transportMode, distance: rt.distanceMeters,
               duration: rt.durationMin, cost: rt.estimatedCost}) AS routes,
       collect(DISTINCT gi) AS insights,
       collect(DISTINCT rr) AS reviews,
       collect(DISTINCT cd) AS climate
`

const cypherGetWeekSubgraph = `
MATCH (week:Week {id: $nodeID})
OPTIONAL MATCH (week)-[:HAS_DAY]->(day:Day)
OPTIONAL MATCH (week)-[:REVIEWED_BY]->(rr:ReviewResult)
RETURN week,
       collect(DISTINCT day {.id, .date, .dayIndex, .theme, .primaryArea, .intensity, .status}) AS days,
       collect(DISTINCT rr) AS reviews
`

const cypherGetMonthSubgraph = `
MATCH (month:Month {id: $nodeID})
OPTIONAL MATCH (month)-[:HAS_WEEK]->(week:Week)
OPTIONAL MATCH (month)-[:REVIEWED_BY]->(rr:ReviewResult)
RETURN month,
       collect(DISTINCT week {.id, .name, .seq, .primaryLocation, .theme,
               .restDayCount, .transferDayCount, .highIntensityDayCount, .status}) AS weeks,
       collect(DISTINCT rr) AS reviews
`

const cypherGetPhaseSubgraph = `
MATCH (phase:Phase {id: $nodeID})
OPTIONAL MATCH (phase)-[:HAS_MONTH]->(month:Month)
OPTIONAL MATCH (phase)-[:HAS_SEASONAL_EVENT]->(se:SeasonalEvent)
OPTIONAL MATCH (phase)-[:REVIEWED_BY]->(rr:ReviewResult)
RETURN phase,
       collect(DISTINCT month {.id, .name, .yearMonth, .seq, .region, .primaryCity,
               .weekCount, .monthlyBudget, .status}) AS months,
       collect(DISTINCT se) AS seasonalEvents,
       collect(DISTINCT rr) AS reviews
`

// --- Read: children ---

const cypherGetChildren = `
MATCH (parent {id: $parentID})
OPTIONAL MATCH (parent)-[r]->(child)
WHERE type(r) STARTS WITH 'HAS_'
RETURN collect(child {.id, .name, .seq, .status, labels: labels(child)}) AS children
`

// --- Read: trip overview ---

const cypherGetTripOverview = `
MATCH (tp:TripPlan {id: $tripPlanID})
OPTIONAL MATCH (tp)-[:HAS_PHASE]->(phase:Phase)
OPTIONAL MATCH (phase)-[:HAS_MONTH]->(month:Month)
OPTIONAL MATCH (month)-[:HAS_WEEK]->(week:Week)
OPTIONAL MATCH (week)-[:HAS_DAY]->(day:Day)
RETURN tp,
       collect(DISTINCT phase {.id, .name, .seq, .region, .season, .status, .dayCount}) AS phases,
       collect(DISTINCT month {.id, .name, .yearMonth, .seq, .region, .status, .weekCount}) AS months,
       collect(DISTINCT week {.id, .name, .seq, .primaryLocation, .status}) AS weeks,
       collect(DISTINCT day {.id, .date, .dayIndex, .theme, .status}) AS days
`

// --- Read: weather context ---

const cypherGetWeatherContext = `
MATCH (cd:ClimateData {region: $region})
WHERE cd.month = $month
OPTIONAL MATCH (wc:WeatherConstraint {region: $region, month: $month})
OPTIONAL MATCH (se:SeasonalEvent {region: $region})
WHERE se.startMonth <= $month AND se.endMonth >= $month
RETURN collect(DISTINCT cd) AS climateData,
       collect(DISTINCT wc) AS constraints,
       collect(DISTINCT se) AS seasonalEvents
`

// --- Read: unplanned nodes ---

const cypherGetUnplannedNodes = `
MATCH (parent {id: $parentID})-[r]->(child)
WHERE type(r) STARTS WITH 'HAS_' AND child.status <> 'done' AND child.status <> 'reviewed'
RETURN collect(child {.id, .name, .status, labels: labels(child)}) AS unplanned
`

// --- Read: layer review status ---

const cypherGetLayerReviewStatus = `
MATCH (parent {id: $parentID})-[r]->(child)
WHERE type(r) STARTS WITH 'HAS_'
OPTIONAL MATCH (child)-[:REVIEWED_BY]->(rr:ReviewResult)
RETURN collect({
    nodeID: child.id,
    nodeName: child.name,
    status: child.status,
    reviewPassed: rr.passed,
    reviewScore: rr.score
}) AS reviewStatus
`

// --- Read: constraint violations ---

const cypherGetConstraintViolations = `
MATCH (root {id: $nodeID})
OPTIONAL MATCH (root)-[:REVIEWED_BY]->(rr:ReviewResult {passed: false})
WITH root,
    collect({nodeID: root.id, nodeName: coalesce(root.name, ''),
        level: rr.level, criticalIssues: rr.criticalIssues,
        violations: rr.constraintViolations}) AS direct
OPTIONAL MATCH (root)-[:HAS_PHASE|HAS_MONTH|HAS_WEEK|HAS_DAY|VISITS_POI*1..6]->(child)
OPTIONAL MATCH (child)-[:REVIEWED_BY]->(cr:ReviewResult {passed: false})
WITH direct + collect({nodeID: child.id, nodeName: coalesce(child.name, ''),
    level: cr.level, criticalIssues: cr.criticalIssues,
    violations: cr.constraintViolations}) AS allViolations
UNWIND allViolations AS v
WITH v WHERE v.level IS NOT NULL
RETURN collect(v) AS violations
`

// --- Read: budget ---

const cypherGetNodeBudgetSummary = `
MATCH (n {id: $nodeID})
OPTIONAL MATCH (n)-[:HAS_PHASE|HAS_MONTH|HAS_WEEK|HAS_DAY|VISITS_POI*1..6]->(child)
WHERE child:POI OR child:RouteSegment
RETURN coalesce(sum(child.estimatedCost), 0) AS totalCost
`

// --- Sequence edges ---

const cypherCreateSequenceEdges = `
MATCH (parent {id: $parentID})
OPTIONAL MATCH (parent)-[r]->(child)
WHERE type(r) STARTS WITH 'HAS_'
WITH parent, child
ORDER BY child.seq
WITH parent, collect(child) AS children
UNWIND range(0, size(children)-2) AS i
WITH children[i] AS current, children[i+1] AS next
CREATE (current)-[:%s]->(next)
`

// FormatSplitCypher builds the split query for a specific parent→child pair.
func FormatSplitCypher(childType string) string {
	relType := relTypeForChild(childType)
	return fmt.Sprintf(cypherSplitParent, relType, childType)
}

func relTypeForChild(childType string) string {
	switch childType {
	case NodeTypePhase:
		return RelHasPhase
	case NodeTypeMonth:
		return RelHasMonth
	case NodeTypeWeek:
		return RelHasWeek
	case NodeTypeDay:
		return RelHasDay
	default:
		return "HAS_" + childType
	}
}

// FormatSequenceCypher builds the sequence-edge query for a child type.
func FormatSequenceCypher(childType string) string {
	relType := nextRelType(childType)
	return fmt.Sprintf(cypherCreateSequenceEdges, relType)
}

func nextRelType(childType string) string {
	switch childType {
	case NodeTypePhase:
		return RelNextPhase
	case NodeTypeMonth:
		return RelNextMonth
	case NodeTypeWeek:
		return RelNextWeek
	case NodeTypeDay:
		return RelNextDay
	default:
		return "NEXT_"
	}
}