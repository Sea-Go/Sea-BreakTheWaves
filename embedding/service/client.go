package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sea/config"
	types "sea/type"
	"sea/zlog"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"go.uber.org/zap"
)

// 配置统一从 config.yaml 读取，避免在代码里散落常量。

// ImageContent 表示单张图片输入。
type ImageContent = types.ImageContent

// MultiImageContent 表示多张图片输入。
type MultiImageContent = types.MultiImageContent

// MultimodalInput 是多模态向量化的请求结构。
type MultimodalInput = types.MultimodalInput

// MultimodalRequest 表示多模态向量化请求（图片/文本）
type MultimodalRequest = types.MultimodalRequest

// rawMultimodalResponse represents the raw API response from DashScope multimodal API
type rawMultimodalResponse struct {
	Output struct {
		Embeddings []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"embeddings"`
	} `json:"output"`
	Usage struct {
		TotalTokens int64 `json:"total_tokens"`
	} `json:"usage"`
}

var httpClient = &http.Client{}

// getEmbeddingConfig returns the current embedding configuration
func getEmbeddingConfig() *config.AliConfig {
	return &config.Cfg.Ali
}

// getTextClient returns a text embedding client
func getTextClient() openai.Client {
	cfg := &config.Cfg.Ali
	return openai.NewClient(
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(cfg.BaseURL),
	)
}

// EmbeddingTxt creates text embedding using OpenAI SDK
func EmbeddingTxt(txt string) (*openai.CreateEmbeddingResponse, error) {
	cfg := getEmbeddingConfig()
	client := getTextClient()
	res, err := client.Embeddings.New(context.TODO(), openai.EmbeddingNewParams{
		Input: openai.EmbeddingNewParamsInputUnion{
			OfString: openai.String(txt),
		},
		Model:          cfg.TextModel,
		Dimensions:     openai.Int(int64(cfg.Dimensions)),
		EncodingFormat: openai.EmbeddingNewParamsEncodingFormatFloat,
		User:           openai.String("user-neo"),
	})
	if err != nil {
		zlog.L().Error("文本向量化失败", zap.Error(err))
		return nil, fmt.Errorf("文本向量化失败: %w", err)
	}
	return res, nil
}

// EmbeddingImage creates embedding from a single image URL using qwen2.5-vl-embedding
func EmbeddingImage(imageURL string) (*openai.CreateEmbeddingResponse, error) {
	aliConfig := getEmbeddingConfig()
	req := MultimodalRequest{
		Model: aliConfig.MultimodalModel,
		Input: MultimodalInput{
			Contents: []interface{}{ImageContent{Image: imageURL}},
		},
		Parameters: struct {
			Dimension string `json:"dimension"`
		}{
			Dimension: fmt.Sprintf("%d", aliConfig.Dimensions),
		},
	}
	return sendMultimodalRequest(req)
}

// EmbeddingMultiImages creates embedding from multiple image URLs using qwen2.5-vl-embedding
func EmbeddingMultiImages(imageURLs []string) (*openai.CreateEmbeddingResponse, error) {
	aliConfig := getEmbeddingConfig()
	req := MultimodalRequest{
		Model: aliConfig.MultimodalModel,
		Input: MultimodalInput{
			Contents: []interface{}{MultiImageContent{MultiImages: imageURLs}},
		},
		Parameters: struct {
			Dimension string `json:"dimension"`
		}{
			Dimension: fmt.Sprintf("%d", aliConfig.Dimensions),
		},
	}
	return sendMultimodalRequest(req)
}

// EmbeddingGraph maintains compatibility with original function signature
// Now delegates to appropriate function based on content type
func EmbeddingGraph(ty string, url string) (*openai.CreateEmbeddingResponse, error) {
	switch ty {
	case "image":
		return EmbeddingImage(url)
	case "multi_images":
		// For multi_images, url should be a JSON array string
		var urls []string
		if err := json.Unmarshal([]byte(url), &urls); err != nil {
			return nil, fmt.Errorf("invalid multi_images URL format: %w", err)
		}
		return EmbeddingMultiImages(urls)
	default:
		return nil, fmt.Errorf("unsupported content type: %s. Supported types: image, multi_images", ty)
	}
}

// sendMultimodalRequest sends the HTTP request to the multimodal API and converts response
func sendMultimodalRequest(req MultimodalRequest) (*openai.CreateEmbeddingResponse, error) {
	aliConfig := getEmbeddingConfig()
	jsonData, err := json.Marshal(req)
	if err != nil {
		zlog.L().Error("failed to marshal multimodal request", zap.Error(err))
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", aliConfig.MultimodalBaseURL, bytes.NewBuffer(jsonData))
	if err != nil {
		zlog.L().Error("failed to create multimodal request", zap.Error(err))
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+aliConfig.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		zlog.L().Error("failed to execute multimodal request", zap.Error(err))
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		zlog.L().Error("failed to read multimodal response body", zap.Error(err))
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		err := fmt.Errorf("API returned error status: %d, body: %s", httpResp.StatusCode, string(body))
		zlog.L().Error("multimodal API error", zap.Error(err))
		return nil, err
	}

	// Parse raw response
	var raw rawMultimodalResponse
	err = json.Unmarshal(body, &raw)
	if err != nil {
		zlog.L().Error("failed to unmarshal multimodal response", zap.Error(err))
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Convert to openai.CreateEmbeddingResponse format
	embeddings := make([]openai.Embedding, 0, len(raw.Output.Embeddings))
	for _, emb := range raw.Output.Embeddings {
		embeddings = append(embeddings, openai.Embedding{
			Embedding: emb.Embedding,
			Index:     int64(emb.Index),
			Object:    "embedding",
		})
	}

	response := &openai.CreateEmbeddingResponse{
		Data:   embeddings,
		Model:  aliConfig.MultimodalModel,
		Object: "list",
		Usage: openai.CreateEmbeddingResponseUsage{
			PromptTokens: raw.Usage.TotalTokens,
			TotalTokens:  raw.Usage.TotalTokens,
		},
	}

	return response, nil
}
