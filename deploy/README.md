# BTW 隔离环境

本目录是 API、Worker、PostgreSQL、MinIO、Milvus、Elasticsearch、Neo4j、Kafka 与 ClickHouse 的参考部署。所有模型运行与选择经 DataCenter 一个地址完成；DataCenter 的受控推理后端在其自身部署中运行。配置和数据使用独立卷，不复用已有项目数据库。

启动前提供 `BTW_DATACENTER_URL`，并由部署层将 DataCenter 工作负载凭据挂载到 `DATACENTER_WORKLOAD_TOKEN_FILE` 指定的本机文件。BTW 的 YAML 只保存 DataCenter 地址，不保存服务凭据或模型名称。还需提供 PostgreSQL、S3、Elasticsearch、Neo4j 和 ClickHouse 的凭据环境变量；不要写入仓库。Elasticsearch 与 Neo4j 只在容器网络内对 API 和 Worker 开放。

```sh
cd deploy
docker compose up -d postgres minio object-init etcd milvus elasticsearch neo4j kafka clickhouse
docker compose run --rm worker -command prepare
docker compose run --rm worker -command build-index
docker compose up -d api worker ingest feedback-export feedback-aggregate
```

`prepare` 建立当前环境的 Milvus、Elasticsearch 索引和 Neo4j 约束；`build-index` 从完整来源重建检索投影并追赶期间的来源变化。两条命令不在应用内登记或激活模型。新环境全部验证后，由部署平台切换入口；回退切回上一个 Git 提交对应的整套环境。原文、来源业务修订和历史数据库不会因此删除。

API 和 Worker 启动时从 DataCenter manifest 读取当前表示模型、空间、维度、检索集合、排序器及生成逻辑别名；生成请求使用 DataCenter 的幂等、额度和审计链。缺失或失效的 DataCenter 工作负载身份会让模型调用明确失败，不会回退到本地模型。DataCenter 负责物理模型与推理后端的配置；BTW 仍负责自身原文、物品、训练样本和检索索引。

DataCenter 的服务账号、模型运行配置及后端部署见其 `docs/MODEL_RUNTIME.md`。改变表示空间须重编码、重建 BTW 索引并整体切流；旧表示空间的请求应由 DataCenter 明确拒绝。

本机没有可用的 Docker/Podman，本目录的 Elasticsearch、Neo4j、Milvus 等组合部署尚未经过物理启动验收。测试结果和缺口见 `docs/architecture/validation.md`。
