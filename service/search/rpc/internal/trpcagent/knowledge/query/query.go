// Package query owns Search query normalization and enhancement contracts.
package query

type Enhancer interface {
	Enhance(query string) (string, error)
}
