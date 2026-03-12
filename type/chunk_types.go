package types

// Article 表示一篇结构化文章（用于推荐系统入库）。
type Article struct {
	ArticleID string   `json:"article_id"`
	Title     string   `json:"title"`
	Cover     string   `json:"cover"`
	TypeTags  []string `json:"type_tags"`
	Tags      []string `json:"tags"`
	Score     float64  `json:"score"`

	Sections []Section `json:"sections"`
}

type Section struct {
	H2     string  `json:"h2"`
	Blocks []Block `json:"blocks"`
}

type Block struct {
	Type     string `json:"type"` // text / image
	Text     string `json:"text"`
	ImageURL string `json:"image_url"`
}

// Chunk 是“精召回”的最小证据单元。
type Chunk struct {
	ChunkID   string `json:"chunk_id"`
	ArticleID string `json:"article_id"`
	H2        string `json:"h2"`
	Content   string `json:"content"`
	Tokens    int    `json:"tokens"`
}

// SplitResult 是切分输出：粗召回文本 + 精召回 chunk 列表。
type SplitResult struct {
	CoarseText string   `json:"coarse_text"`
	FineChunks []Chunk  `json:"fine_chunks"`
	Keywords   []string `json:"keywords"`
}
