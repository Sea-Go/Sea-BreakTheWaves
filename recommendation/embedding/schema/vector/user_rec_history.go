package schema

import "github.com/milvus-io/milvus/client/v2/entity"

// UserRecHistoryTableName 创建“用户推荐历史”向量集合 Schema。
// - 用于在用户历史中做相似检索（去重/偏好分析）。
// - 事实记录仍以 PG 为准（时间戳/点击/偏好等）。
func UserRecHistoryTableName(collectionName string, dim int) *entity.Schema {
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

	articleID := entity.NewField().
		WithName("article_id").
		WithDataType(entity.FieldTypeVarChar).
		WithTypeParams(entity.TypeParamMaxLength, "128")

	clicked := entity.NewField().
		WithName("clicked").
		WithDataType(entity.FieldTypeBool)

	preference := entity.NewField().
		WithName("preference").
		WithDataType(entity.FieldTypeFloat)

	tsUnix := entity.NewField().
		WithName("ts_unix").
		WithDataType(entity.FieldTypeInt64)

	return entity.NewSchema().
		WithName(collectionName).
		WithDescription("用户推荐历史向量集合").
		WithAutoID(false).
		WithDynamicFieldEnabled(true).
		WithField(id).
		WithField(vec).
		WithField(userID).
		WithField(articleID).
		WithField(clicked).
		WithField(preference).
		WithField(tsUnix)
}
