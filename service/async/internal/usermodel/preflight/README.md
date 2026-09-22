# BTW usermodel GORM schema 只读预检

本包固定 `service/async/internal/usermodel/schema.go` 生成的 PostgreSQL 物理结构契约，用于在启用 SubjectRef v2 或迁移候选前审核 scoped 表结构和聚合计数。它不执行 DDL、不写业务行、不回显 DSN。逐表真实列、PK、FK、唯一键见 [物理结构清单](SCHEMA_INVENTORY.md)。

## 使用

```bash
go run ./service/async/rpc/cmd/usermodel-subjectref-preflight \
  --run --dsn-env BTW_PREFLIGHT_PG_DSN --schema public
```

`--run` 与 `--dsn-env` 必须显式指定。CLI 将会话设为 read-only，并在同一笔 `REPEATABLE READ READ ONLY` 事务中读取 catalog 与受限计数。`--mode catalog` 仅用于隔离 PG16 结构契约生成。

## 契约生成

```bash
bash service/async/internal/usermodel/preflight/generate-contract.sh
python3 service/async/internal/usermodel/preflight/render-inventory.py
```

`generate-contract.sh` 会启动临时 PostgreSQL，先通过 `service/async/rpc/cmd/usermodel-schema` 执行 GORM `AutoMigrate`，再读取 23 张 scoped 核心表生成 `contract.json`。`source_sha256` 是 GORM schema 源文件 SHA-256；源文件变化后必须重新生成契约并审查差异。

## 验证

```bash
bash service/async/internal/usermodel/preflight/test-postgres.sh
```

脚本使用随机 schema 的隔离 PostgreSQL，执行 race 测试、vet、CLI 默认关闭检查，以及 GORM 初始化后的空库审计。
