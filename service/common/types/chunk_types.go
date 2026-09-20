package types

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

type Chunk struct {
	ChunkID     string   `json:"chunk_id"`
	ArticleID   string   `json:"article_id"`
	H2          string   `json:"h2"`
	Content     string   `json:"content"`
	Tokens      int      `json:"tokens"`
	ContentType string   `json:"content_type,omitempty"` // text / image / multi_images
	ImageURLs   []string `json:"image_urls,omitempty"`
}

type SplitResult struct {
	CoarseText    string   `json:"coarse_text"`
	CoarseRawText string   `json:"coarse_raw_text"`
	CoarseIntro   string   `json:"coarse_intro"`
	KeywordScore  float32  `json:"keyword_score"`
	FineChunks    []Chunk  `json:"fine_chunks"`
	ImageChunks   []Chunk  `json:"image_chunks"`
	Keywords      []string `json:"keywords"`
}
