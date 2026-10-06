package ranking

import (
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"sort"
	"strings"
)

var FeatureNames = []string{"dense", "sparse", "multi", "rrf", "graph_overlap", "quality", "quality_missing"}

var SearchFeatureNames = []string{"elastic", "graph", "dense", "sparse", "multi", "rrf", "graph_overlap", "quality", "quality_missing", "elastic_missing", "multi_missing"}

func Terms(o *item.Ontology) []string {
	if o == nil {
		return nil
	}
	terms := map[string]bool{}
	labels := map[string]string{o.UserID: "user"}
	addAttributes := func(prefix string, attrs map[string]any) {
		for key, value := range attrs {
			switch v := value.(type) {
			case string:
				if v != "" {
					terms[strings.TrimSpace(prefix+" "+key+" "+v)] = true
				}
			case float64, bool, int:
				terms[strings.TrimSpace(prefix+" "+key+" "+fmt.Sprint(v))] = true
			case []any:
				for _, entry := range v {
					if text, ok := entry.(string); ok {
						terms[strings.TrimSpace(prefix+" "+key+" "+text)] = true
					}
				}
			default:
				if value != nil {
					terms[strings.TrimSpace(prefix+" "+key+" "+fmt.Sprint(value))] = true
				}
			}
		}
	}
	for _, node := range o.Nodes {
		label := strings.TrimSpace(node.Label)
		labels[node.ID] = label
		if label != "" {
			terms[label] = true
		}
		addAttributes(label, node.Attributes)
	}
	for _, edge := range o.Relations {
		from, to := labels[edge.From], labels[edge.To]
		if from != "" && to != "" {
			phrase := from + " " + edge.Type + " " + to
			terms[phrase] = true
			addAttributes(phrase, edge.Attributes)
		}
	}
	addAttributes("", o.Attributes)
	out := make([]string, 0, len(terms))
	for term := range terms {
		out = append(out, term)
	}
	sort.Strings(out)
	return out
}

func Features(h item.Hit, o *item.Ontology) []float64 {
	overlap := 0.
	text := strings.ToLower(h.Item.Title + " " + h.Item.Summary + " " + strings.Join(h.Item.Topics, " "))
	for _, term := range Terms(o) {
		if strings.Contains(text, strings.ToLower(term)) {
			overlap++
		}
	}
	quality, ok := h.Item.Features["quality"]
	missing := 0.
	if !ok {
		missing = 1
	}
	return []float64{h.Scores["dense"], h.Scores["sparse"], h.Scores["multi"], h.Scores["rrf"], overlap, quality, missing}
}

func SearchFeatures(h item.Hit, o *item.Ontology) []float64 {
	shared := Features(h, o)
	_, lexical := h.Scores["elastic"]
	_, multi := h.Scores["multi"]
	missingLexical, missingMulti := 0.0, 0.0
	if !lexical {
		missingLexical = 1
	}
	if !multi {
		missingMulti = 1
	}
	return []float64{h.Scores["elastic"], h.Scores["graph"], shared[0], shared[1], shared[2], shared[3], shared[4], shared[5], shared[6], missingLexical, missingMulti}
}
