// Package recommend embeds the explicit WS08-E immutable pool migration.
package recommend

import _ "embed"

//go:embed 001_pools.sql
var SQL string
