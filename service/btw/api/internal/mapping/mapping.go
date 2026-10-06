package mapping

import (
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/types"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
)

func Request(v *types.QueryRequest) item.Request {
	r := item.Request{RequestID: v.RequestId, Scenario: v.Scenario, SearchLevel: v.SearchLevel, Query: v.Query, Context: v.Context, Limit: v.Limit}
	if v.Ontology != nil {
		o := v.Ontology
		r.Ontology = &item.Ontology{UserID: o.UserId, Attributes: o.Attributes}
		for _, n := range o.Nodes {
			r.Ontology.Nodes = append(r.Ontology.Nodes, item.Node{ID: n.Id, Type: n.Type, Label: n.Label, Attributes: n.Attributes})
		}
		for _, e := range o.Relations {
			r.Ontology.Relations = append(r.Ontology.Relations, item.Edge{From: e.From, To: e.To, Type: e.Type, Attributes: e.Attributes})
		}
		if o.Vector != nil {
			r.Ontology.Vector = &item.UserVector{Space: o.Vector.Space, Values: o.Vector.Values}
		}
	}
	return r
}
func Location(v item.Location) types.Location {
	return types.Location{RevisionId: v.RevisionID, SectionId: v.SectionID, Start: v.Start, End: v.End}
}
func Item(v item.Item) types.Item {
	out := types.Item{Id: v.ID, RootId: v.RootID, Kind: v.Kind, RootKind: v.RootKind, SourceId: v.SourceID, RevisionId: v.RevisionID, Title: v.Title, Summary: v.Summary, SourceUrl: v.SourceURL, DuplicateId: v.DuplicateID, Features: v.Features, Topics: v.Topics, Location: Location(v.Location), Language: v.Language}
	for _, r := range v.Relations {
		out.Relations = append(out.Relations, types.Relation{From: r.From, To: r.To, Type: r.Type, Location: Location(r.Location)})
	}
	return out
}

func Result(v item.Result) *types.Result {
	out := &types.Result{RequestId: v.RequestID, Status: v.Status, Path: v.Path, Answer: v.Answer, Warnings: v.Warnings, Hits: []types.Hit{}, Citations: []types.Citation{}}
	for _, h := range v.Hits {
		out.Hits = append(out.Hits, types.Hit{Item: Item(h.Item), Score: h.Score, Scores: h.Scores, Lanes: h.Lanes})
	}
	for _, c := range v.Citations {
		out.Citations = append(out.Citations, types.Citation{Id: c.ID, ItemId: c.ItemID, Title: c.Title, Location: Location(c.Location), Quote: c.Quote, Url: c.URL})
	}
	if v.Plan != nil {
		out.Plan = &types.Plan{Name: v.Plan.Name, Goal: v.Plan.Goal, Steps: []types.PlanStep{}}
		for _, step := range v.Plan.Steps {
			out.Plan.Steps = append(out.Plan.Steps, types.PlanStep{Action: step.Action, CitationIds: step.CitationIDs})
		}
	}
	return out
}
func Event(e item.StreamEvent) *types.StreamEvent {
	v := &types.StreamEvent{Type: e.Type, RequestId: e.RequestID, Delta: e.Delta, Error: e.Error}
	if e.Result != nil {
		v.Result = Result(*e.Result)
	}
	return v
}
