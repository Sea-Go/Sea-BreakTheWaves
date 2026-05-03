package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sea/config"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetEmbeddingConfig 测试配置获取函数
func TestGetEmbeddingConfig(t *testing.T) {
	// 设置测试配置
	testConfig := `milvus:
  address: "localhost:19530"
  username: ""
  password: ""
  dbname: "ChatTest"

ali:
  apikey: "ChatTest-router-key"
  baseurl: "https://dashscope.aliyuncs.com/compatible-mode/v1"
  multimodal_baseurl: "https://dashscope.aliyuncs.com/router/v1/services/embeddings/multimodal-embedding/multimodal-embedding"
  text_model: "text-embedding-v4"
  multimodal_model: "qwen2.5-vl-embedding"
  dimensions: 2048

Kafka:
  address: "localhost:39092"

neo4j:
  address: "neo4j://localhost:37687"
  username: "neo4j"
  password: "Sea-TryGo"
`

	// 临时创建配置文件
	configFile := filepath.Join(t.TempDir(), "test_config.yaml")
	err := writeConfigFile(configFile, testConfig)
	require.NoError(t, err)

	// 加载测试配置
	err = config.Load(configFile)
	require.NoError(t, err)

	// 测试配置获取
	cfg := getEmbeddingConfig()
	assert.NotNil(t, cfg)
	assert.Equal(t, "ChatTest-router-key", cfg.APIKey)
	assert.Equal(t, "https://dashscope.aliyuncs.com/compatible-mode/v1", cfg.BaseURL)
	assert.Equal(t, "https://dashscope.aliyuncs.com/router/v1/services/embeddings/multimodal-embedding/multimodal-embedding", cfg.MultimodalBaseURL)
	assert.Equal(t, "text-embedding-v4", cfg.TextModel)
	assert.Equal(t, "qwen2.5-vl-embedding", cfg.MultimodalModel)
	assert.Equal(t, 2048, cfg.Dimensions)

	// 清理
	cleanup(configFile)
}

// TestGetTextClient 测试文本客户端创建
func TestGetTextClient(t *testing.T) {
	setupTestConfig(t)

	client := getTextClient()
	assert.NotNil(t, client)
}

// TestMultimodalRequestSerialization 测试多模态请求序列化
func TestMultimodalRequestSerialization(t *testing.T) {
	setupTestConfig(t)

	// 测试单图片请求
	singleImageReq := MultimodalRequest{
		Model: "qwen2.5-vl-embedding",
		Input: MultimodalInput{
			Contents: []interface{}{ImageContent{Image: "https://example.com/image.jpg"}},
		},
		Parameters: struct {
			Dimension string `json:"dimension"`
		}{
			Dimension: "2048",
		},
	}

	jsonData, err := json.Marshal(singleImageReq)
	assert.NoError(t, err)
	assert.NotNil(t, jsonData)

	// 验证JSON内容
	jsonStr := string(jsonData)
	assert.Contains(t, jsonStr, "qwen2.5-vl-embedding")
	assert.Contains(t, jsonStr, "https://example.com/image.jpg")
	assert.Contains(t, jsonStr, "2048")

	// 测试多图片请求
	multiImageReq := MultimodalRequest{
		Model: "qwen2.5-vl-embedding",
		Input: MultimodalInput{
			Contents: []interface{}{MultiImageContent{MultiImages: []string{
				"https://example.com/image1.jpg",
				"https://example.com/image2.jpg",
			}}},
		},
		Parameters: struct {
			Dimension string `json:"dimension"`
		}{
			Dimension: "2048",
		},
	}

	jsonData, err = json.Marshal(multiImageReq)
	assert.NoError(t, err)
	assert.Contains(t, string(jsonData), "multi_images")
}

// TestImageContentStruct 测试图片内容结构
func TestImageContentStruct(t *testing.T) {
	content := ImageContent{Image: "https://example.com/image.jpg"}

	// 测试JSON序列化
	jsonData, err := json.Marshal(content)
	assert.NoError(t, err)

	// 验证JSON格式
	var parsed ImageContent
	err = json.Unmarshal(jsonData, &parsed)
	assert.NoError(t, err)
	assert.Equal(t, content.Image, parsed.Image)

	// 验证JSON键名
	jsonStr := string(jsonData)
	assert.Contains(t, jsonStr, `"image"`)
}

// TestMultiImageContentStruct 测试多图片内容结构
func TestMultiImageContentStruct(t *testing.T) {
	imageURLs := []string{
		"https://example.com/image1.jpg",
		"https://example.com/image2.jpg",
		"https://example.com/image3.jpg",
	}

	content := MultiImageContent{MultiImages: imageURLs}

	// 测试JSON序列化
	jsonData, err := json.Marshal(content)
	assert.NoError(t, err)

	// 测试反序列化
	var parsed MultiImageContent
	err = json.Unmarshal(jsonData, &parsed)
	assert.NoError(t, err)
	assert.Equal(t, imageURLs, parsed.MultiImages)

	// 验证JSON键名
	jsonStr := string(jsonData)
	assert.Contains(t, jsonStr, `"multi_images"`)
}

// TestMultimodalInputStruct 测试多模态输入结构
func TestMultimodalInputStruct(t *testing.T) {
	contents := []interface{}{
		ImageContent{Image: "https://example.com/image1.jpg"},
		MultiImageContent{MultiImages: []string{"https://example.com/image2.jpg"}},
	}

	input := MultimodalInput{Contents: contents}

	// 测试序列化
	jsonData, err := json.Marshal(input)
	assert.NoError(t, err)

	// 测试反序列化
	var parsed MultimodalInput
	err = json.Unmarshal(jsonData, &parsed)
	assert.NoError(t, err)
	assert.Equal(t, len(contents), len(parsed.Contents))
}

// TestRawMultimodalResponseStruct 测试原始响应结构
func TestRawMultimodalResponseStruct(t *testing.T) {
	// 创建测试响应数据
	testResponse := rawMultimodalResponse{
		Output: struct {
			Embeddings []struct {
				Index     int       `json:"index"`
				Embedding []float64 `json:"embedding"`
			} `json:"embeddings"`
		}{
			Embeddings: []struct {
				Index     int       `json:"index"`
				Embedding []float64 `json:"embedding"`
			}{
				{
					Index:     0,
					Embedding: make([]float64, 2048),
				},
				{
					Index:     1,
					Embedding: make([]float64, 2048),
				},
			},
		},
		Usage: struct {
			TotalTokens int64 `json:"total_tokens"`
		}{
			TotalTokens: 150,
		},
	}

	// 测试序列化
	jsonData, err := json.Marshal(testResponse)
	assert.NoError(t, err)

	// 测试反序列化
	var parsed rawMultimodalResponse
	err = json.Unmarshal(jsonData, &parsed)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(parsed.Output.Embeddings))
	assert.Equal(t, 0, parsed.Output.Embeddings[0].Index)
	assert.Equal(t, 1, parsed.Output.Embeddings[1].Index)
	assert.Equal(t, 2048, len(parsed.Output.Embeddings[0].Embedding))
	assert.Equal(t, int64(150), parsed.Usage.TotalTokens)
}

// TestEmbeddingGraphTypeHandling 测试EmbeddingGraph的类型处理
func TestEmbeddingGraphTypeHandling(t *testing.T) {
	setupTestConfig(t)

	tests := []struct {
		name       string
		ty         string
		url        string
		expectErr  bool
		expectType string
	}{
		{
			name:       "图片类型",
			ty:         "image",
			url:        "https://example.com/image.jpg",
			expectErr:  true, // 没有真实API调用，会返回错误
			expectType: "image",
		},
		{
			name:       "多图片类型 - 有效JSON",
			ty:         "multi_images",
			url:        `["https://example.com/image1.jpg", "https://example.com/image2.jpg"]`,
			expectErr:  true, // 没有真实API调用
			expectType: "multi_images",
		},
		{
			name:       "多图片类型 - 无效JSON",
			ty:         "multi_images",
			url:        "invalid json",
			expectErr:  true,
			expectType: "",
		},
		{
			name:       "不支持的类型",
			ty:         "video",
			url:        "https://example.com/video.mp4",
			expectErr:  true,
			expectType: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 测试函数调用和错误处理逻辑
			_, err := EmbeddingGraph(tt.ty, tt.url)

			if tt.expectErr {
				assert.Error(t, err, "应该返回错误")
			} else {
				assert.NoError(t, err, "不应该返回错误")
			}

			// 对于支持的内容类型，验证JSON解析逻辑
			if tt.ty == "multi_images" && !tt.expectErr {
				var urls []string
				err := json.Unmarshal([]byte(tt.url), &urls)
				assert.NoError(t, err, "有效的多图片JSON应该能够解析")
				assert.NotEmpty(t, urls, "解析后的URL列表不应为空")
			}
		})
	}
}

// TestEmbeddingImageRequestCreation 测试EmbeddingImage请求创建
func TestEmbeddingImageRequestCreation(t *testing.T) {
	setupTestConfig(t)

	imageURL := "https://example.com/test-image.jpg"

	// 由于无法直接访问sendMultimodalRequest，我们通过验证配置来间接测试
	cfg := getEmbeddingConfig()
	assert.Equal(t, "qwen2.5-vl-embedding", cfg.MultimodalModel)
	assert.Equal(t, 2048, cfg.Dimensions)
	assert.NotEmpty(t, cfg.MultimodalBaseURL)
	assert.NotEmpty(t, cfg.APIKey)

	// 测试图片内容结构
	imageContent := ImageContent{Image: imageURL}
	jsonData, err := json.Marshal(imageContent)
	assert.NoError(t, err)
	assert.Contains(t, string(jsonData), imageURL)
}

// TestEmbeddingMultiImagesRequestCreation 测试EmbeddingMultiImages请求创建
func TestEmbeddingMultiImagesRequestCreation(t *testing.T) {
	setupTestConfig(t)

	imageURLs := []string{
		"https://example.com/image1.jpg",
		"https://example.com/image2.jpg",
	}

	// 测试多图片内容结构
	multiImageContent := MultiImageContent{MultiImages: imageURLs}
	jsonData, err := json.Marshal(multiImageContent)
	assert.NoError(t, err)
	assert.Contains(t, string(jsonData), "image1.jpg")
	assert.Contains(t, string(jsonData), "image2.jpg")
}

// TestConfigurationValues 测试配置值是否正确
func TestConfigurationValues(t *testing.T) {
	setupTestConfig(t)

	cfg := getEmbeddingConfig()

	// 验证关键配置值
	assert.Equal(t, "ChatTest-router-key", cfg.APIKey)
	assert.Equal(t, "https://dashscope.aliyuncs.com/compatible-mode/v1", cfg.BaseURL)
	assert.Equal(t, "https://dashscope.aliyuncs.com/router/v1/services/embeddings/multimodal-embedding/multimodal-embedding", cfg.MultimodalBaseURL)
	assert.Equal(t, "text-embedding-v4", cfg.TextModel)
	assert.Equal(t, "qwen2.5-vl-embedding", cfg.MultimodalModel)
	assert.Equal(t, 2048, cfg.Dimensions)
}

// TestEmbeddingGraphJSONParsing 测试EmbeddingGraph的JSON解析
func TestEmbeddingGraphJSONParsing(t *testing.T) {
	tests := []struct {
		name        string
		jsonStr     string
		expectError bool
		expectCount int
	}{
		{
			name:        "单张图片",
			jsonStr:     `["https://example.com/image.jpg"]`,
			expectError: false,
			expectCount: 1,
		},
		{
			name:        "多张图片",
			jsonStr:     `["https://example.com/image1.jpg", "https://example.com/image2.jpg"]`,
			expectError: false,
			expectCount: 2,
		},
		{
			name:        "空数组",
			jsonStr:     `[]`,
			expectError: false,
			expectCount: 0,
		},
		{
			name:        "无效JSON",
			jsonStr:     "invalid json",
			expectError: true,
			expectCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var urls []string
			err := json.Unmarshal([]byte(tt.jsonStr), &urls)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectCount, len(urls))
			}
		})
	}
}

// 辅助函数

// setupTestConfig 设置测试配置
func setupTestConfig(t *testing.T) {
	testConfig := `milvus:
  address: "localhost:19530"
  username: ""
  password: ""
  dbname: "ChatTest"

ali:
  apikey: "ChatTest-router-key"
  baseurl: "https://dashscope.aliyuncs.com/compatible-mode/v1"
  multimodal_baseurl: "https://dashscope.aliyuncs.com/router/v1/services/embeddings/multimodal-embedding/multimodal-embedding"
  text_model: "text-embedding-v4"
  multimodal_model: "qwen2.5-vl-embedding"
  dimensions: 2048

Kafka:
  address: "localhost:39092"

neo4j:
  address: "neo4j://localhost:37687"
  username: "neo4j"
  password: "Sea-TryGo"
`

	configFile := filepath.Join(t.TempDir(), "test_config_unit.yaml")
	err := writeConfigFile(configFile, testConfig)
	require.NoError(t, err)

	err = config.Load(configFile)
	require.NoError(t, err)

	t.Cleanup(func() {
		cleanup(configFile)
	})
}

// writeConfigFile 写入配置文件
func writeConfigFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

// cleanup 清理文件
func cleanup(path string) {
	os.Remove(path)
}
