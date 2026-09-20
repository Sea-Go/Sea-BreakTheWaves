package graph

import (
	"context"
	"fmt"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// schemaConstraints 定义 12 类节点的唯一性约束（id 或 name 唯一）。
// 通过 IF NOT EXISTS + 列出现有约束双重幂等，可安全重复执行。
var schemaConstraints = []struct {
	name   string
	cypher string
}{
	// 文章节点：以业务 id 唯一标识，覆盖内容/作者/频道/IP 维度。
	{"article_id_unique", "CREATE CONSTRAINT article_id_unique IF NOT EXISTS FOR (n:Article) REQUIRE n.id IS UNIQUE"},
	// 标签节点：以 name 唯一，避免重复标签产生歧义。
	{"tag_name_unique", "CREATE CONSTRAINT tag_name_unique IF NOT EXISTS FOR (n:Tag) REQUIRE n.name IS UNIQUE"},
	// 类型标签节点：以 name 唯一，区分主类型（如科技/娱乐/体育）。
	{"type_tag_name_unique", "CREATE CONSTRAINT type_tag_name_unique IF NOT EXISTS FOR (n:TypeTag) REQUIRE n.name IS UNIQUE"},
	// 作者节点：以 id 唯一，支撑 WRITTEN_BY 关系与作者维度召回。
	{"author_id_unique", "CREATE CONSTRAINT author_id_unique IF NOT EXISTS FOR (n:Author) REQUIRE n.id IS UNIQUE"},
	// IP 节点：以 name 唯一，支撑 BELONGS_TO_IP 关系与频道独立池。
	{"ip_name_unique", "CREATE CONSTRAINT ip_name_unique IF NOT EXISTS FOR (n:IP) REQUIRE n.name IS UNIQUE"},
	// 频道节点：以 name 唯一，IP 频道注册体系核心。
	{"channel_name_unique", "CREATE CONSTRAINT channel_name_unique IF NOT EXISTS FOR (n:Channel) REQUIRE n.name IS UNIQUE"},
	// 主题节点：以 name 唯一，用于话题聚类与召回。
	{"topic_name_unique", "CREATE CONSTRAINT topic_name_unique IF NOT EXISTS FOR (n:Topic) REQUIRE n.name IS UNIQUE"},
	// 实体节点：以 id 唯一，实体链接结果落地。
	{"entity_id_unique", "CREATE CONSTRAINT entity_id_unique IF NOT EXISTS FOR (n:Entity) REQUIRE n.id IS UNIQUE"},
	// 图片节点：以 id 唯一，含视觉向量属性，支撑 VISUALLY_SIMILAR 关系。
	{"image_id_unique", "CREATE CONSTRAINT image_id_unique IF NOT EXISTS FOR (n:Image) REQUIRE n.id IS UNIQUE"},
	// 用户节点：以 id 唯一，用户画像与行为关联。
	{"user_id_unique", "CREATE CONSTRAINT user_id_unique IF NOT EXISTS FOR (n:User) REQUIRE n.id IS UNIQUE"},
	// 行为节点：以 id 唯一，PERFORMED 关系与行为画像聚合。
	{"behavior_id_unique", "CREATE CONSTRAINT behavior_id_unique IF NOT EXISTS FOR (n:Behavior) REQUIRE n.id IS UNIQUE"},
	// 质量报告节点：以 id 唯一，HAS_QUALITY_REPORT 关系与质量阈值。
	{"quality_report_id_unique", "CREATE CONSTRAINT quality_report_id_unique IF NOT EXISTS FOR (n:QualityReport) REQUIRE n.id IS UNIQUE"},
}

// schemaIndexes 定义 8 类索引（高频查询字段加速）。
// 通过 IF NOT EXISTS + 列出现有索引双重幂等，可安全重复执行。
var schemaIndexes = []struct {
	name   string
	cypher string
}{
	// 文章按 user_id 索引：用户历史文章查询。
	{"article_user_id_index", "CREATE INDEX article_user_id_index IF NOT EXISTS FOR (n:Article) ON (n.user_id)"},
	// 文章按 channel 索引：频道召回池。
	{"article_channel_index", "CREATE INDEX article_channel_index IF NOT EXISTS FOR (n:Article) ON (n.channel)"},
	// 文章按 author_id 索引：作者维度召回。
	{"article_author_id_index", "CREATE INDEX article_author_id_index IF NOT EXISTS FOR (n:Article) ON (n.author_id)"},
	// 文章按 create_time 索引：时效性召回与衰减。
	{"article_create_time_index", "CREATE INDEX article_create_time_index IF NOT EXISTS FOR (n:Article) ON (n.create_time)"},
	// 用户按 channel 索引：频道独立画像 filterKey。
	{"user_channel_index", "CREATE INDEX user_channel_index IF NOT EXISTS FOR (n:User) ON (n.channel)"},
	// 行为按 user_id 索引：行为画像聚合查询。
	{"behavior_user_id_index", "CREATE INDEX behavior_user_id_index IF NOT EXISTS FOR (n:Behavior) ON (n.user_id)"},
	// 行为按 created_at 索引：时间窗口行为分析。
	{"behavior_created_at_index", "CREATE INDEX behavior_created_at_index IF NOT EXISTS FOR (n:Behavior) ON (n.created_at)"},
	// 图片按 article_id 索引：图片-文章关联查询。
	{"image_article_id_index", "CREATE INDEX image_article_id_index IF NOT EXISTS FOR (n:Image) ON (n.article_id)"},
}

// EnsureSchema 幂等创建 Neo4j 唯一性约束与索引。
// 先通过 SHOW CONSTRAINTS / SHOW INDEXES 列出现有对象，再对缺失项执行 CREATE ... IF NOT EXISTS。
// 多次调用安全（幂等），用于服务启动初始化。
func (c *Client) EnsureSchema(ctx context.Context) error {
	existingConstraints, err := c.listConstraints(ctx)
	if err != nil {
		return fmt.Errorf("graph: list constraints: %w", err)
	}
	existingIndexes, err := c.listIndexes(ctx)
	if err != nil {
		return fmt.Errorf("graph: list indexes: %w", err)
	}

	// 创建缺失的唯一性约束。
	for _, ct := range schemaConstraints {
		if existingConstraints[ct.name] {
			continue
		}
		if _, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			_, err := tx.Run(ctx, ct.cypher, nil)
			return nil, err
		}); err != nil {
			return fmt.Errorf("graph: create constraint %s: %w", ct.name, err)
		}
	}

	// 创建缺失的索引。
	for _, ix := range schemaIndexes {
		if existingIndexes[ix.name] {
			continue
		}
		if _, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			_, err := tx.Run(ctx, ix.cypher, nil)
			return nil, err
		}); err != nil {
			return fmt.Errorf("graph: create index %s: %w", ix.name, err)
		}
	}

	return nil
}

// listConstraints 列出当前数据库所有约束的 name 集合，用于幂等判断。
func (c *Client) listConstraints(ctx context.Context) (map[string]bool, error) {
	result, err := c.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		rec, err := tx.Run(ctx, "SHOW CONSTRAINTS", nil)
		if err != nil {
			return nil, err
		}
		var names []string
		for rec.Next(ctx) {
			if name, ok := rec.Record().Get("name"); ok {
				names = append(names, fmt.Sprint(name))
			}
		}
		return names, rec.Err()
	})
	if err != nil {
		return nil, err
	}
	names, _ := result.([]string)
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set, nil
}

// listIndexes 列出当前数据库所有索引的 name 集合，用于幂等判断。
func (c *Client) listIndexes(ctx context.Context) (map[string]bool, error) {
	result, err := c.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		rec, err := tx.Run(ctx, "SHOW INDEXES", nil)
		if err != nil {
			return nil, err
		}
		var names []string
		for rec.Next(ctx) {
			if name, ok := rec.Record().Get("name"); ok {
				names = append(names, fmt.Sprint(name))
			}
		}
		return names, rec.Err()
	})
	if err != nil {
		return nil, err
	}
	names, _ := result.([]string)
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set, nil
}
