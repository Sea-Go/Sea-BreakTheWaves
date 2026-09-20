// Package filter declares the Search-owned RAG filter contract; the concrete
// metadata filter is kept beside the retrieval engine until transport split.
package filter

type Request struct {
	Tags             []string
	Authors          []string
	Channel          string
	QualityThreshold float64
}
