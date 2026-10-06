# 内容与原文

电子书原文件、完整正文及自然章节保存在对象存储，PostgreSQL 保存来源业务修订、自然结构、物品、关系和可恢复任务。网站内容通过 Kafka 传入，不抓取来源 URL。处理失败仍保留已接纳原文。

Worker 在当前部署环境中建设 Elasticsearch 全文、Neo4j 内容图和 Milvus 表示索引。更新时旧可检索修订保留至新内容完成；删除由 PostgreSQL 立即隐藏，索引清理由 Worker 重试。引用始终以保存的正文回读核对。

接口、上传与 Worker 命令见 [运行合同](architecture/operations.md)。
