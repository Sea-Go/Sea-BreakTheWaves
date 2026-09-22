# usermodel GORM schema preflight acceptance

- Isolated PostgreSQL 16 is initialized only by `usermodel.MigrateSchema`.
- The scoped physical contract covers the 23 core usermodel tables and 19 foreign keys.
- Empty schema audit returns L1/L2/L3 = 0 and completes all 23 table scans.
- The CLI is default-off and its audit session is repeatable-read/read-only.
- `contract.json` source SHA tracks `service/async/rpc/internal/usermodel/schema.go`.
