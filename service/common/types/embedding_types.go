package types

// ImageContent 表示单张图片输入。
type ImageContent struct {
	Image string `json:"image"`
}

// MultiImageContent 表示多张图片输入。
type MultiImageContent struct {
	MultiImages []string `json:"multi_images"`
}

// MultimodalInput 是多模态向量化的请求结构。
type MultimodalInput struct {
	Contents []interface{} `json:"contents"`
}

// MultimodalRequest 表示多模态向量化请求（图片/文本）。
// Parameters.Dimension 使用 string 是为了对齐 DashScope API 的入参类型。
type MultimodalRequest struct {
	Model      string          `json:"model"`
	Input      MultimodalInput `json:"input"`
	Parameters struct {
		Dimension string `json:"dimension"`
	} `json:"parameters"`
}

// GraphRAG

type ParentNode struct {
	NodeID    string
	ArticleID string
	ChunkID   string
	Title     string
	Tag       string
	Keywords  []string
}

type ChildNode struct {
	NodeID   string
	ChunkID  string
	Title    string
	Tag      string
	Keywords []string
}

type Edge struct {
	EdgeID     string
	FromNodeID string
	ToNodeID   string
	Weight     float64
	Tag        string
}

type Neighbor struct {
	ChunkID string
	Weight  float64
	Rel     string
}
