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

func Milvus() *milvusclient.Client {
	return milvusCli
}

func MilvusInit() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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

	dbName := strings.TrimSpace(config.Cfg.Milvus.DBName)
	if dbName == "" {
		dbName = "default"
	}
	if err := ensureDatabase(cli, dbName); err != nil {
		return err
	}

	useCtx, useCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer useCancel()
	if err := cli.UseDatabase(useCtx, milvusclient.NewUseDatabaseOption(dbName)); err != nil {
		zlog.L().Error("切换 Milvus 数据库失败", zap.Error(err), zap.String("db", config.Cfg.Milvus.DBName))
		return err
	}

	milvusCli = cli

	coarseName := config.Cfg.Milvus.Collections.Coarse
	fineName := config.Cfg.Milvus.Collections.Fine
	imageName := strings.TrimSpace(config.Cfg.Milvus.Collections.Image)
	if imageName == "" {
		imageName = "recall_image"
	}
	memoryChunksName := "user_memory_chunks"
	userHistoryName := "user_rec_history"
	dim := config.Cfg.Milvus.Collections.Dim
	metric := parseMetric(config.Cfg.Milvus.Collections.Metric)

	zlog.L().Info(
		"Milvus 客户端初始化完成，集合初始化后台执行",
		zap.String("db", config.Cfg.Milvus.DBName),
		zap.String("coarse", coarseName),
		zap.String("fine", fineName),
		zap.String("image", imageName),
		zap.String("memory_chunks", memoryChunksName),
		zap.String("user_history", userHistoryName),
		zap.Int("dim", dim),
	)

	go func() {
		initCtx, initCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer initCancel()

		if err := ensureCollection(initCtx, coarseName, vectorschema.RecllCandidateTableName(coarseName, dim), metric); err != nil {
			zlog.L().Warn("Milvus 集合初始化失败", zap.Error(err), zap.String("collection", coarseName))
			return
		}
		if err := ensureCollection(initCtx, fineName, vectorschema.RecallPreciseTableName(fineName, dim), metric); err != nil {
			zlog.L().Warn("Milvus 集合初始化失败", zap.Error(err), zap.String("collection", fineName))
			return
		}
		if err := ensureCollection(initCtx, imageName, vectorschema.RecallPreciseTableName(imageName, dim), metric); err != nil {
			zlog.L().Warn("Milvus 集合初始化失败", zap.Error(err), zap.String("collection", imageName))
			return
		}
		if err := ensureCollection(initCtx, memoryChunksName, vectorschema.UserMemoryChunksTableName(memoryChunksName, dim), metric); err != nil {
			zlog.L().Warn("Milvus 集合初始化失败", zap.Error(err), zap.String("collection", memoryChunksName))
			return
		}
		if err := ensureCollection(initCtx, userHistoryName, vectorschema.UserRecHistoryTableName(userHistoryName, dim), metric); err != nil {
			zlog.L().Warn("Milvus 集合初始化失败", zap.Error(err), zap.String("collection", userHistoryName))
			return
		}

		zlog.L().Info(
			"Milvus 集合初始化完成",
			zap.String("db", config.Cfg.Milvus.DBName),
			zap.String("coarse", coarseName),
			zap.String("fine", fineName),
			zap.String("image", imageName),
			zap.String("memory_chunks", memoryChunksName),
			zap.String("user_history", userHistoryName),
			zap.Int("dim", dim),
		)
	}()
	return nil
}

func ensureDatabase(cli *milvusclient.Client, dbName string) error {
	listCtx, listCancel := context.WithTimeout(context.Background(), 30*time.Second)
	databases, err := cli.ListDatabase(listCtx, milvusclient.NewListDatabaseOption())
	listCancel()
	if err != nil {
		zlog.L().Warn("列出 Milvus 数据库失败，尝试直接创建", zap.Error(err), zap.String("db", dbName))
	} else {
		for _, name := range databases {
			if name == dbName {
				return nil
			}
		}
	}

	createCtx, createCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer createCancel()
	if err := cli.CreateDatabase(createCtx, milvusclient.NewCreateDatabaseOption(dbName)); err != nil {
		zlog.L().Warn("创建 Milvus 数据库失败，可能已存在", zap.Error(err), zap.String("db", dbName))
	}
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

		idx := index.NewAutoIndex(metric)
		if _, err := milvusCli.CreateIndex(ctx, milvusclient.NewCreateIndexOption(collectionName, "vector", idx)); err != nil {
			zlog.L().Error("创建向量索引失败", zap.Error(err), zap.String("collection", collectionName))
			return err
		}
	}

	if collectionName == config.Cfg.Milvus.Collections.Fine || collectionName == config.Cfg.Milvus.Collections.Image {
		if err := ensureDocumentField(ctx, collectionName); err != nil {
			return err
		}
	}

	if _, err := milvusCli.LoadCollection(ctx, milvusclient.NewLoadCollectionOption(collectionName)); err != nil {
		zlog.L().Error("加载集合失败", zap.Error(err), zap.String("collection", collectionName))
		return err
	}
	return nil
}

func ensureDocumentField(ctx context.Context, collectionName string) error {
	collection, err := milvusCli.DescribeCollection(ctx, milvusclient.NewDescribeCollectionOption(collectionName))
	if err != nil {
		zlog.L().Error("读取集合 schema 失败", zap.Error(err), zap.String("collection", collectionName))
		return err
	}
	if collection != nil && collection.Schema != nil {
		for _, field := range collection.Schema.Fields {
			if field != nil && field.Name == "document" {
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
		zlog.L().Error("追加 document 字段失败", zap.Error(err), zap.String("collection", collectionName))
		return err
	}
	zlog.L().Info("已为集合追加 document 字段", zap.String("collection", collectionName))
	return nil
}
