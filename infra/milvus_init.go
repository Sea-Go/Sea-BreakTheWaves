package infra

import (
	"context"
	"strconv"
	"strings"
	"time"

	"sea/config"
	vectorschema "sea/embedding/schema/vector"
	"sea/zlog"

	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/index"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"go.uber.org/zap"
)

var milvusCli *milvusclient.Client

// Milvus 返回全局 Milvus 客户端（在 MilvusInit 成功后可用）。
func Milvus() *milvusclient.Client {
	return milvusCli
}

// MilvusInit 初始化 Milvus：连接、创建/切换数据库、确保集合存在并加载到内存。
func MilvusInit() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cli, err := milvusclient.New(ctx, &milvusclient.ClientConfig{
		Address:  config.Cfg.Milvus.Address,
		Username: config.Cfg.Milvus.Username,
		Password: config.Cfg.Milvus.Password,
	})
	if err != nil {
		zlog.L().Error("连接 Milvus 失败", zap.Error(err))
		return err
	}

	// 数据库
	if err := cli.CreateDatabase(ctx, milvusclient.NewCreateDatabaseOption(config.Cfg.Milvus.DBName)); err != nil {
		// 数据库已存在会报错，这里不作为致命错误
		zlog.L().Warn("创建 Milvus 数据库失败（可能已存在）", zap.Error(err), zap.String("db", config.Cfg.Milvus.DBName))
	}
	if err := cli.UseDatabase(ctx, milvusclient.NewUseDatabaseOption(config.Cfg.Milvus.DBName)); err != nil {
		zlog.L().Error("切换 Milvus 数据库失败", zap.Error(err), zap.String("db", config.Cfg.Milvus.DBName))
		return err
	}

	milvusCli = cli

	// 确保集合存在
	coarseName := config.Cfg.Milvus.Collections.Coarse
	fineName := config.Cfg.Milvus.Collections.Fine
	// 额外集合：用户记忆分块 / 用户推荐历史（避免 pgvector，统一走 Milvus）
	memoryChunksName := "user_memory_chunks"
	userHistoryName := "user_rec_history"
	dim := config.Cfg.Milvus.Collections.Dim

	metric := parseMetric(config.Cfg.Milvus.Collections.Metric)

	if err := ensureCollection(ctx, coarseName, vectorschema.RecllCandidateTableName(coarseName, dim), metric); err != nil {
		return err
	}
	if err := ensureCollection(ctx, fineName, vectorschema.RecallPreciseTableName(fineName, dim), metric); err != nil {
		return err
	}
	if err := ensureCollection(ctx, memoryChunksName, vectorschema.UserMemoryChunksTableName(memoryChunksName, dim), metric); err != nil {
		return err
	}
	if err := ensureCollection(ctx, userHistoryName, vectorschema.UserRecHistoryTableName(userHistoryName, dim), metric); err != nil {
		return err
	}

	zlog.L().Info(
		"Milvus 初始化完成",
		zap.String("db", config.Cfg.Milvus.DBName),
		zap.String("粗召回集合", coarseName),
		zap.String("精召回集合", fineName),
		zap.String("记忆分块集合", memoryChunksName),
		zap.String("用户历史集合", userHistoryName),
		zap.Int("dim", dim),
	)
	return nil
}

func parseMetric(metricStr string) entity.MetricType {
	switch strings.ToUpper(strings.TrimSpace(metricStr)) {
	case "L2":
		return entity.L2
	case "IP":
		return entity.IP
	case "COSINE":
		return entity.COSINE
	default:
		return entity.COSINE
	}
}

func ensureCollection(ctx context.Context, collectionName string, schema *entity.Schema, metric entity.MetricType) error {
	exist, err := milvusCli.HasCollection(ctx, milvusclient.NewHasCollectionOption(collectionName))
	if err != nil {
		zlog.L().Error("检查集合是否存在失败", zap.Error(err), zap.String("collection", collectionName))
		return err
	}
	if !exist {
		zlog.L().Info("集合不存在，开始创建", zap.String("collection", collectionName))
		if err := milvusCli.CreateCollection(ctx, milvusclient.NewCreateCollectionOption(collectionName, schema)); err != nil {
			zlog.L().Error("创建集合失败", zap.Error(err), zap.String("collection", collectionName))
			return err
		}

		// 创建向量索引（这里用 AUTOINDEX，方便开箱即用；生产可按数据规模调参）
		idx := index.NewAutoIndex(metric)
		_, err := milvusCli.CreateIndex(ctx, milvusclient.NewCreateIndexOption(collectionName, "vector", idx))
		if err != nil {
			zlog.L().Error("创建向量索引失败", zap.Error(err), zap.String("collection", collectionName))
			return err
		}
	}

	if collectionName == config.Cfg.Milvus.Collections.Fine {
		if err := ensureFineCollectionDocumentField(ctx, collectionName); err != nil {
			return err
		}
	}

	// load
	_, err = milvusCli.LoadCollection(ctx, milvusclient.NewLoadCollectionOption(collectionName))
	if err != nil {
		zlog.L().Error("加载集合失败", zap.Error(err), zap.String("collection", collectionName))
		return err
	}
	return nil
}

func ensureFineCollectionDocumentField(ctx context.Context, collectionName string) error {
	collection, err := milvusCli.DescribeCollection(ctx, milvusclient.NewDescribeCollectionOption(collectionName))
	if err != nil {
		zlog.L().Error("读取集合 schema 失败", zap.Error(err), zap.String("collection", collectionName))
		return err
	}
	if collection != nil && collection.Schema != nil {
		for _, f := range collection.Schema.Fields {
			if f != nil && f.Name == "document" {
				return nil
			}
		}
	}

	newField := entity.NewField().
		WithName("document").
		WithDataType(entity.FieldTypeVarChar).
		WithTypeParams(entity.TypeParamMaxLength, strconv.Itoa(65535)).
		WithNullable(true)

	if err := milvusCli.AddCollectionField(ctx, milvusclient.NewAddCollectionFieldOption(collectionName, newField)); err != nil {
		zlog.L().Error("为精召回集合追加 document 字段失败", zap.Error(err), zap.String("collection", collectionName))
		return err
	}
	zlog.L().Info("已为精召回集合追加 document 字段", zap.String("collection", collectionName))
	return nil
}
