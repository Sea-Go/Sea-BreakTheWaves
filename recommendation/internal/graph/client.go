package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// ============================================================================
// 该文件定义 Neo4j 客户端 Client（实现 domain.GraphQuerier 的查询能力）。
//
// 注意：本文件为最小可编译占位（stub），仅提供 Client 结构、构造器与事务包装。
// TODO(Task 2.2): 由 Task 2.2 完善 Schema 初始化、连接管理、Operations
// （upsert/link）、EntityLink、ImageSearch 等完整实现。
//
// QueryByCypher / RecallByGraph 及 7 类 Cypher 模板定义于 cypher.go。
// ============================================================================

// Client Neo4j 客户端，承载 driver 并提供事务包装。
// 实现 domain.GraphQuerier 的 QueryByCypher / RecallByGraph（见 cypher.go）。
// TODO(Task 2.2): 补全 EntityLink / ImageSearch 实现，使其完整满足 interface。
type Client struct {
	driver neo4j.DriverWithContext
}

// NewClient 创建 Neo4j 客户端。
//
// 通过 uri/username/password 内部构造 neo4j.DriverWithContext，不调用
// VerifyConnectivity，因此即使 Neo4j 服务未启动也能成功构造（便于测试与离线编译）。
// 真正的连接建立发生在首次查询时（惰性连接）。
//
// 参数：
//   - uri: Neo4j Bolt 协议地址，如 "bolt://localhost:7687"
//   - username: 用户名，通常为 "neo4j"
//   - password: 密码
//
// TODO(Task 2.2): 完善 URI 校验、连接池配置、日志/可观测性。
func NewClient(uri, username, password string) (*Client, error) {
	driver, err := neo4j.NewDriverWithContext(uri, neo4j.BasicAuth(username, password, ""))
	if err != nil {
		return nil, fmt.Errorf("graph: create neo4j driver: %w", err)
	}
	return &Client{driver: driver}, nil
}

// Close 关闭底层 Neo4j driver，释放连接池资源。
// 多次调用安全（driver 内部幂等）。
func (c *Client) Close(ctx context.Context) error {
	if c == nil || c.driver == nil {
		return nil
	}
	if err := c.driver.Close(ctx); err != nil {
		return fmt.Errorf("graph: close driver: %w", err)
	}
	return nil
}

// ExecuteRead 执行读事务包装，封装 NewSession + ExecuteRead 样板代码。
// fn 内可通过 tx.Run 执行 Cypher 并迭代结果。
// TODO(Task 2.2): 完善超时/重试/错误处理与可观测性。
func (c *Client) ExecuteRead(ctx context.Context, fn func(tx neo4j.ManagedTransaction) (any, error)) (any, error) {
	if c == nil || c.driver == nil {
		return nil, errors.New("neo4j driver is nil")
	}
	sess := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer sess.Close(ctx)
	return sess.ExecuteRead(ctx, fn)
}

// ExecuteWrite 执行写事务包装，封装 NewSession + ExecuteWrite 样板代码。
// TODO(Task 2.2): 完善超时/重试/错误处理与可观测性。
func (c *Client) ExecuteWrite(ctx context.Context, fn func(tx neo4j.ManagedTransaction) (any, error)) (any, error) {
	if c == nil || c.driver == nil {
		return nil, errors.New("neo4j driver is nil")
	}
	sess := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer sess.Close(ctx)
	return sess.ExecuteWrite(ctx, fn)
}
