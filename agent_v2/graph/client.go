package graph

import (
	"context"
	"fmt"

	"agent_v2/config"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// client is the package-level Neo4j client, initialized by Init.
var client *Client

// Client wraps the Neo4j driver with connection-pool management.
type Client struct {
	driver neo4j.DriverWithContext
	dbName string
	cfg    config.Neo4jConfig
}

// Init initializes the package-level Neo4j client from global config.
// Panics if Neo4j is enabled but the connection cannot be established.
func Init() {
	c, err := NewClient(config.Cfg.Neo4j)
	if err != nil {
		panic(fmt.Sprintf("graph: failed to initialize Neo4j client: %v", err))
	}
	client = c
}

// GetClient returns the package-level Neo4j client, or nil if not enabled.
func GetClient() *Client {
	return client
}

// NewClient creates a new Neo4j client. Returns nil if config is not enabled.
func NewClient(cfg config.Neo4jConfig) (*Client, error) {
	cfg = cfg.WithDefaults()
	if !cfg.Enabled {
		return nil, nil
	}

	driver, err := neo4j.NewDriverWithContext(
		cfg.URI,
		neo4j.BasicAuth(cfg.Username, cfg.Password, ""),
		func(c *neo4j.Config) {
			c.MaxConnectionPoolSize = cfg.MaxPoolSize
		},
	)
	if err != nil {
		return nil, fmt.Errorf("neo4j: create driver: %w", err)
	}

	if err := driver.VerifyConnectivity(context.Background()); err != nil {
		driver.Close(context.Background())
		return nil, fmt.Errorf("neo4j: verify connectivity: %w", err)
	}

	return &Client{driver: driver, dbName: cfg.Database, cfg: cfg}, nil
}

// NewSession creates a new session with the configured database.
func (c *Client) NewSession(ctx context.Context) neo4j.SessionWithContext {
	return c.driver.NewSession(ctx, neo4j.SessionConfig{
		DatabaseName: c.dbName,
	})
}

// ExecuteWrite runs a write transaction.
func (c *Client) ExecuteWrite(ctx context.Context, fn func(tx neo4j.ManagedTransaction) (any, error)) (any, error) {
	session := c.NewSession(ctx)
	defer session.Close(ctx)
	return session.ExecuteWrite(ctx, fn)
}

// ExecuteRead runs a read transaction.
func (c *Client) ExecuteRead(ctx context.Context, fn func(tx neo4j.ManagedTransaction) (any, error)) (any, error) {
	session := c.NewSession(ctx)
	defer session.Close(ctx)
	return session.ExecuteRead(ctx, fn)
}

// Close shuts down the driver.
func (c *Client) Close(ctx context.Context) error {
	return c.driver.Close(ctx)
}

// IsEnabled reports whether the Neo4j client is active.
func (c *Client) IsEnabled() bool {
	return c != nil && c.cfg.Enabled
}

// MinDaysForSplit returns the configured minimum day threshold for split mode.
func (c *Client) MinDaysForSplit() int {
	if c == nil {
		return 2
	}
	return c.cfg.MinDaysForSplit
}