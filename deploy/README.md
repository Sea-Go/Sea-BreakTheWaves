# Deploy

本目录保存本地基础设施编排和 Docker 挂载配置。

启动基础依赖：

```sh
docker compose -f deploy/docker-compose.yaml up -d
```

目录内容：

- `docker-compose.yaml`：PostgreSQL、Redis、Neo4j、Kafka、MinIO、Milvus、观测组件等本地依赖编排。
- `prometheus/settings.yml`：Prometheus 抓取配置，由 Compose 挂载。
- `otel-collector-config.yaml`：OpenTelemetry Collector 配置，由 Compose 挂载。

默认卷和数据目录会相对本目录创建；如需放到其他磁盘路径，设置：

```sh
DOCKER_VOLUME_DIRECTORY=/path/to/sea-deploy-data
```
