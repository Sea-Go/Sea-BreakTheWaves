package runtime

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/session/postgres"
)

// PostgresConfig identifies framework-owned tables. Automatic schema creation is
// explicit; steady-state processes should use an already initialized schema.
type PostgresConfig struct {
	DSN         string
	Schema      string
	TablePrefix string
	Initialize  bool
}

// OpenPostgres owns the session connection and closes it after all runs exit.
func OpenPostgres(app string, ag agent.Agent, cfg PostgresConfig, observed *telemetry.Bundle) (*Runtime, error) {
	if strings.TrimSpace(cfg.DSN) == "" || cfg.Schema == "" || cfg.TablePrefix == "" {
		return nil, errors.New("explicit session DSN, schema and prefix required")
	}
	svc, err := postgres.NewService(postgres.WithPostgresClientDSN(cfg.DSN), postgres.WithSchema(cfg.Schema), postgres.WithTablePrefix(cfg.TablePrefix), postgres.WithSkipDBInit(!cfg.Initialize), postgres.WithEnableAsyncPersist(false))
	if err != nil {
		return nil, fmt.Errorf("open framework postgres session: %w", err)
	}
	result, err := New(app, ag, svc, observed)
	if err != nil {
		_ = svc.Close()
		return nil, err
	}
	result.ownedSession = true
	return result, nil
}
