package doc_ingest

import (
	"context"
	"fmt"
	"strings"

	"sea/config"
	graphschema "sea/embedding/schema/graph"
	"sea/infra"
	"sea/storage"
	"sea/zlog"

	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"go.uber.org/zap"
)

func DeleteArticleState(ctx context.Context, articleRepo *storage.ArticleRepo, articleID string) error {
	articleID = strings.TrimSpace(articleID)
	if articleID == "" {
		return fmt.Errorf("article_id is empty")
	}
	if articleRepo == nil {
		return fmt.Errorf("article repo is nil")
	}

	if err := articleRepo.DeleteArticle(ctx, articleID); err != nil {
		return err
	}

	if cli := infra.Milvus(); cli != nil {
		collections := []string{
			strings.TrimSpace(config.Cfg.Milvus.Collections.Coarse),
			strings.TrimSpace(config.Cfg.Milvus.Collections.Fine),
			strings.TrimSpace(config.Cfg.Milvus.Collections.Image),
		}
		for _, collection := range collections {
			if strings.TrimSpace(collection) == "" {
				continue
			}
			if err := deleteByArticleID(ctx, cli, collection, articleID); err != nil {
				return err
			}
		}
	}

	if drv := infra.Neo4j(); drv != nil {
		store := graphschema.NewStore(drv)
		if store != nil {
			if err := store.DeleteArticle(ctx, articleID); err != nil {
				zlog.L().Warn("delete article graph state failed", zap.Error(err), zap.String("article_id", articleID))
				return err
			}
		}
	}

	return nil
}

func deleteByArticleID(ctx context.Context, cli *milvusclient.Client, collection, articleID string) error {
	expr := fmt.Sprintf(`article_id == "%s"`, escapeMilvusString(articleID))
	_, err := cli.Delete(ctx, milvusclient.NewDeleteOption(collection).WithExpr(expr))
	return err
}

func escapeMilvusString(in string) string {
	in = strings.ReplaceAll(in, `\`, `\\`)
	in = strings.ReplaceAll(in, `"`, `\"`)
	return in
}
