package schema

import "github.com/milvus-io/milvus/client/v2/entity"

// UserMemoryChunksTableName 创建“用户记忆分块”向量集合 Schema。
// - 用于长期/周期记忆 tokenize 分块后的向量索引。
// - 仅用于召回最相关的记忆片段；原文仍以 PG 为准（便于审计/可追溯）。
func UserMemoryChunksTableName(collectionName string, dim int) *entity.Schema {
	id := entity.NewField().
		WithName("id").
		WithDataType(entity.FieldTypeVarChar).
		WithTypeParams(entity.TypeParamMaxLength, "256").
		WithIsPrimaryKey(true).
		WithIsAutoID(false)

	vec := entity.NewField().
		WithName("vector").
		WithDataType(entity.FieldTypeFloatVector).
		WithDim(int64(dim))

	userID := entity.NewField().
		WithName("user_id").
		WithDataType(entity.FieldTypeVarChar).
		WithTypeParams(entity.TypeParamMaxLength, "128")

	memoryType := entity.NewField().
		WithName("memory_type").
		WithDataType(entity.FieldTypeVarChar).
		WithTypeParams(entity.TypeParamMaxLength, "64")

	periodBucket := entity.NewField().
		WithName("period_bucket").
		WithDataType(entity.FieldTypeVarChar).
		WithTypeParams(entity.TypeParamMaxLength, "64")

	chunkIndex := entity.NewField().
		WithName("chunk_index").
		WithDataType(entity.FieldTypeInt64)

	versionUnix := entity.NewField().
		WithName("version_unix").
		WithDataType(entity.FieldTypeInt64)

	content := entity.NewField().
		WithName("content").
		WithDataType(entity.FieldTypeVarChar).
		WithTypeParams(entity.TypeParamMaxLength, "8192")

	return entity.NewSchema().
		WithName(collectionName).
		WithDescription("用户记忆分块向量集合").
		WithAutoID(false).
		WithDynamicFieldEnabled(true).
		WithField(id).
		WithField(vec).
		WithField(userID).
		WithField(memoryType).
		WithField(periodBucket).
		WithField(chunkIndex).
		WithField(versionUnix).
		WithField(content)
}
