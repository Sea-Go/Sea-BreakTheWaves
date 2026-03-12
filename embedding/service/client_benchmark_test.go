package service

import (
	"path/filepath"
	"sea/config"
	"testing"
)

// BenchmarkEmbeddingImageRequestCreation 基准测试：图片请求创建
func BenchmarkEmbeddingImageRequestCreation(b *testing.B) {
	setupTestConfigForBenchmark(b)

	imageURL := "https://example.com/test-image.jpg"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = EmbeddingImage(imageURL)
	}
}

// BenchmarkEmbeddingMultiImagesRequestCreation 基准测试：多图片请求创建
func BenchmarkEmbeddingMultiImagesRequestCreation(b *testing.B) {
	setupTestConfigForBenchmark(b)

	imageURLs := []string{
		"https://example.com/image1.jpg",
		"https://example.com/image2.jpg",
		"https://example.com/image3.jpg",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = EmbeddingMultiImages(imageURLs)
	}
}

// BenchmarkEmbeddingGraphTypeSwitch 基准测试：类型切换逻辑
func BenchmarkEmbeddingGraphTypeSwitch(b *testing.B) {
	setupTestConfigForBenchmark(b)

	imageURL := "https://example.com/test-image.jpg"
	multiImagesJSON := `["https://example.com/image1.jpg", "https://example.com/image2.jpg"]`

	tests := []struct {
		ty  string
		url string
	}{
		{"image", imageURL},
		{"multi_images", multiImagesJSON},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = EmbeddingGraph(tests[i%2].ty, tests[i%2].url)
	}
}

// BenchmarkGetEmbeddingConfig 基准测试：配置获取
func BenchmarkGetEmbeddingConfig(b *testing.B) {
	setupTestConfigForBenchmark(b)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = getEmbeddingConfig()
	}
}

// BenchmarkGetTextClient 基准测试：文本客户端获取
func BenchmarkGetTextClient(b *testing.B) {
	setupTestConfigForBenchmark(b)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = getTextClient()
	}
}

// setupTestConfigForBenchmark 设置基准测试配置
func setupTestConfigForBenchmark(b *testing.B) {
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

	configFile := filepath.Join(b.TempDir(), "test_config_benchmark.yaml")
	err := writeConfigFile(configFile, testConfig)
	if err != nil {
		b.Fatal(err)
	}

	err = config.Load(configFile)
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() {
		cleanup(configFile)
	})
}
