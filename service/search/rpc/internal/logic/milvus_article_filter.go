package service

import (
	"fmt"
	"strings"
)

type RecallSearchOptions struct {
	ExcludedArticleIDs []string
}

func buildMilvusArticleFilterExpr(includeArticleIDs []string, excludedArticleIDs []string) string {
	var filters []string
	if includeExpr := buildVarCharInExpr("article_id", includeArticleIDs); includeExpr != "" {
		filters = append(filters, includeExpr)
	}
	if excludeExpr := buildVarCharNotInExpr("article_id", excludedArticleIDs); excludeExpr != "" {
		filters = append(filters, excludeExpr)
	}
	return joinMilvusFilterExprs(filters...)
}

func buildVarCharNotInExpr(field string, ids []string) string {
	vals := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		id = strings.ReplaceAll(id, `\`, `\\`)
		id = strings.ReplaceAll(id, `"`, `\"`)
		vals = append(vals, fmt.Sprintf(`"%s"`, id))
	}
	if len(vals) == 0 {
		return ""
	}
	return fmt.Sprintf("%s not in [%s]", field, strings.Join(vals, ","))
}

func joinMilvusFilterExprs(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, "("+part+")")
	}
	return strings.Join(out, " and ")
}
