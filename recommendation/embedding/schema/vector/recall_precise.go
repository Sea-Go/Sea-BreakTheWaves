package schema

import (
	"github.com/milvus-io/milvus/client/v2/entity"
)

// New精召回Schema 创建“精召回”向量表结构。
// 精召回向量 = 二级标题 + 段落内容（包含图片与文字），尽量避免切得太碎。
func RecallPreciseTableName(collectionName string, dim int) *entity.Schema {
	id := entity.NewField().
		WithName("id").
		WithDataType(entity.FieldTypeVarChar).
		WithTypeParams(entity.TypeParamMaxLength, "128").
		WithIsPrimaryKey(true).
		WithIsAutoID(false)

	vec := entity.NewField().
		WithName("vector").
		WithDataType(entity.FieldTypeFloatVector).
		WithDim(int64(dim))

	articleID := entity.NewField().
		WithName("article_id").
		WithDataType(entity.FieldTypeVarChar).
		WithTypeParams(entity.TypeParamMaxLength, "128")

	chunkID := entity.NewField().
		WithName("chunk_id").
		WithDataType(entity.FieldTypeVarChar).
		WithTypeParams(entity.TypeParamMaxLength, "128")

	h2 := entity.NewField().
		WithName("h2").
		WithDataType(entity.FieldTypeVarChar).
		WithTypeParams(entity.TypeParamMaxLength, "256")

	document := entity.NewField().
		WithName("document").
		WithDataType(entity.FieldTypeVarChar).
		WithTypeParams(entity.TypeParamMaxLength, "65535").
		WithNullable(false)

	// 标签、type 等（逗号拼接）
	tags := entity.NewField().
		WithName("tags").
		WithDataType(entity.FieldTypeVarChar).
		WithTypeParams(entity.TypeParamMaxLength, "512")

	// 文章分数（入池前的基础分）
	score := entity.NewField().
		WithName("score").
		WithDataType(entity.FieldTypeFloat)

	createdAt := entity.NewField().
		WithName("created_at_unix").
		WithDataType(entity.FieldTypeInt64)

	return entity.NewSchema().
		WithName(collectionName).
		WithDescription("精召回向量集合").
		WithAutoID(false).
		WithDynamicFieldEnabled(true).
		WithField(id).
		WithField(vec).
		WithField(articleID).
		WithField(chunkID).
		WithField(h2).
		WithField(document).
		WithField(tags).
		WithField(score).
		WithField(createdAt)
}
