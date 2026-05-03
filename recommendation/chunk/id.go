package chunk

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

var demoLikeArticleIDRe = regexp.MustCompile(`(?i)^(a[_-]?(test|demo)|article[_-]?(test|demo)|test|demo|tmp|temp|sample)([_-].*)?$`)

// NormalizeArticleID returns a stable, realistic-looking article id.
// Rules:
// 1. Keep caller-provided ids unless they are obviously demo/test placeholders.
// 2. Auto-generate a deterministic UUID-style id when missing or placeholder-like.
func NormalizeArticleID(currentID string, a Article) string {
	currentID = strings.TrimSpace(currentID)
	if currentID != "" && !IsDemoLikeArticleID(currentID) {
		return currentID
	}

	seedParts := []string{
		strings.TrimSpace(a.Title),
		strings.TrimSpace(a.Cover),
		strings.Join(a.TypeTags, ","),
	}
	for _, sec := range a.Sections {
		if s := strings.TrimSpace(sec.H2); s != "" {
			seedParts = append(seedParts, s)
		}
		for _, blk := range sec.Blocks {
			if t := strings.TrimSpace(blk.Text); t != "" {
				seedParts = append(seedParts, t)
			}
			if img := strings.TrimSpace(blk.ImageURL); img != "" {
				seedParts = append(seedParts, img)
			}
		}
	}
	if currentID != "" {
		seedParts = append([]string{"input:" + currentID}, seedParts...)
	}
	seed := strings.Join(seedParts, "\n")
	if strings.TrimSpace(seed) == "" {
		seed = currentID
	}

	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("article:"+seed))
	return "art_" + id.String()
}

func IsDemoLikeArticleID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	return demoLikeArticleIDRe.MatchString(id)
}

// BuildChunkID returns a deterministic UUID-style chunk id.
func BuildChunkID(articleID string, seq int, h2, content string) string {
	seed := fmt.Sprintf("%s|%d|%s|%s", strings.TrimSpace(articleID), seq, strings.TrimSpace(h2), strings.TrimSpace(content))
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("chunk:"+seed))
	return "chk_" + id.String()
}
