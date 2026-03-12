package skillsys

import (
	"sea/skills/doc_ingest"
	"sea/skills/memory_manage"
	"sea/skills/milvus_search"
	"sea/skills/pool_manage"
	"sea/skills/rerank"
	"sea/skills/user_history"
	"sea/storage"
)

// RegisterSkills 集中注册所有 skills。
// 目标：main.go 保持干净，只负责装配依赖与启动服务。
func RegisterSkills(
	reg *Registry,
	articleRepo *storage.ArticleRepo,
	poolRepo *storage.PoolRepo,
	memoryRepo *storage.MemoryRepo,
	historyRepo *storage.UserHistoryRepo,
	memoryChunkRepo *storage.MemoryChunkRepo,
) {
	if reg == nil {
		return
	}

	// 向量检索
	reg.Register(milvus_search.New())

	// 文档入库
	reg.Register(doc_ingest.New(articleRepo))

	// 候选池管理
	reg.Register(pool_manage.NewPoolGetSize(poolRepo))
	reg.Register(pool_manage.NewPoolRefill(poolRepo, articleRepo))
	reg.Register(pool_manage.NewPoolPopTopK(poolRepo))

	// 用户历史
	reg.Register(user_history.NewAdd(historyRepo))
	reg.Register(user_history.NewRecent(historyRepo))
	reg.Register(user_history.NewSimilar(historyRepo))

	// 记忆管理
	reg.Register(memory_manage.NewGet(memoryRepo))
	reg.Register(memory_manage.NewUpsert(memoryRepo, memoryChunkRepo))
	reg.Register(memory_manage.NewMaintainWindow(historyRepo, articleRepo, memoryRepo))
	reg.Register(memory_manage.NewChunkHybridSearch(memoryRepo, memoryChunkRepo))

	// 精排序
	reg.Register(rerank.New(articleRepo, memoryRepo))
	reg.Register(rerank.NewDashScope())
}
