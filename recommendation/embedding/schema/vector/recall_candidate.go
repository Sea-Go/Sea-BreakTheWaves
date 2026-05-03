package schema

import (
	"github.com/milvus-io/milvus/client/v2/entity"
)

// New粗召回Schema 创建“粗召回”向量表结构。
// 粗召回向量 = 标题 + 封面 + 手打 type 标签 + 关键词检测 + 各类二级标题
//
// 说明：
// - 开启动态字段（Dynamic Field），便于后续新增 scalar 字段而不必迁移集合。
// - 同时显式声明关键字段（article_id / score / tags 等），便于统一约束与查询。
func RecllCandidateTableName(collectionName string, dim int) *entity.Schema {
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

	// 文章分数（入池前的基础分）
	score := entity.NewField().
		WithName("score").
		WithDataType(entity.FieldTypeFloat)

	// 标签、type 等（逗号拼接）
	tags := entity.NewField().
		WithName("tags").
		WithDataType(entity.FieldTypeVarChar).
		WithTypeParams(entity.TypeParamMaxLength, "512")

	createdAt := entity.NewField().
		WithName("created_at_unix").
		WithDataType(entity.FieldTypeInt64)

	return entity.NewSchema().
		WithName(collectionName).
		WithDescription("粗召回向量集合").
		WithAutoID(false).
		WithDynamicFieldEnabled(true).
		WithField(id).
		WithField(vec).
		WithField(articleID).
		WithField(score).
		WithField(tags).
		WithField(createdAt)
}
