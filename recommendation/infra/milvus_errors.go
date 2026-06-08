package infra

import (
	"errors"
	"strings"
)

// IsMilvusUnavailableError returns true for local/vector-store readiness errors
// that should degrade to empty retrieval instead of surfacing as user-facing 500s.
func IsMilvusUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	for err != nil {
		text := strings.ToLower(err.Error())
		if strings.Contains(text, "milvus 客户端未初始化") ||
			strings.Contains(text, "can't find collection") ||
			strings.Contains(text, "collection not found") ||
			strings.Contains(text, "collection not exist") ||
			strings.Contains(text, "collection is not loaded") {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}
