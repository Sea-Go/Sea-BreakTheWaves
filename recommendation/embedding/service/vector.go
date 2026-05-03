package service

import (
	"context"
	"fmt"

	"sea/config"
	"sea/metrics"
	"sea/zlog"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.uber.org/zap"
)

// TextVector 把文本转成向量（float32 切片）。
//
// ✅ Jaeger 可观测性修复点：
//   - 在函数内部创建 OTel span（embedding.text_vector），这样任何调用方（doc_ingest / milvus_search / pool_refill / memory_manage / user_history...）
//     都能自动在 Jaeger 中看到 embedding 的耗时与错误。
func TextVector(ctx context.Context, text string) ([]float32, error) {
	ali := config.Cfg.Ali

	tr := otel.Tracer("sea/embedding")
	ctx, span := tr.Start(ctx, "embedding.text_vector")
	defer span.End()

	span.SetAttributes(
		attribute.String("embedding.model", ali.TextModel),
		attribute.Int("embedding.dim", ali.Dimensions),
		attribute.Int("embedding.input_len", len(text)),
	)

	client := openai.NewClient(
		option.WithAPIKey(ali.APIKey),
		option.WithBaseURL(ali.BaseURL),
	)

	res, err := client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Input: openai.EmbeddingNewParamsInputUnion{
			OfString: openai.String(text),
		},
		// 注意：Model 是必填的 string/枚举，不要用 openai.String 包装。
		Model: ali.TextModel,
		// openai.Int 的入参是 int64（变量是 int 需要显式转换）。
		Dimensions:     openai.Int(int64(ali.Dimensions)),
		EncodingFormat: openai.EmbeddingNewParamsEncodingFormatFloat,
		User:           openai.String("reco_agent"),
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		zlog.L().Error("文本向量化失败", zap.Error(err))
		return nil, fmt.Errorf("文本向量化失败: %w", err)
	}
	if len(res.Data) == 0 {
		err := fmt.Errorf("文本向量化返回空数据")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	// token metrics（embedding 只有 total_tokens）
	metrics.GenRecAgentTotalTokensMetric.Add(float64(res.Usage.TotalTokens))

	// SDK 返回的是 []float64，这里统一转换成 []float32，方便与 Milvus 的 float vector 对齐。
	vec64 := res.Data[0].Embedding
	vec32 := make([]float32, 0, len(vec64))
	for _, v := range vec64 {
		vec32 = append(vec32, float32(v))
	}

	span.SetStatus(codes.Ok, "")
	return vec32, nil
}
