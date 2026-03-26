package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"

	"sea/config"
	"sea/infra"
	"sea/skills/doc_ingest"
	"sea/skills/memory_manage"
	"sea/skills/milvus_search"
	"sea/skills/pool_manage"
	"sea/skills/rerank"
	"sea/skills/user_history"
	"sea/skillsys"
	"sea/storage"
	"sea/zlog"

	"github.com/openai/openai-go/v3"
	"go.uber.org/zap"
)

// 说明：这是“人机对话式测试”入口。
// 你可以多轮输入问题，观察：
// - 模型如何调用工具（skills）
// - 每次工具调用的入参/出参摘要
// - 最终输出与置信度（模型自评）
//
// 运行：go run ./ChatTest/chat_cli.go
func main() {
	if err := config.Init(); err != nil {
		panic(err)
	}
	if err := zlog.InitLogger(config.Cfg.Log.Path, config.Cfg.Log.Level, config.Cfg.Log.ServiceName); err != nil {
		panic(err)
	}
	ctx := context.Background()

	if err := infra.PostgresInit(); err != nil {
		panic(err)
	}
	if err := infra.MilvusInit(); err != nil {
		panic(err)
	}

	// Repo + Registry
	db := infra.Postgres()
	articleRepo := storage.NewArticleRepo(db)
	poolRepo := storage.NewPoolRepo(db)
	memoryRepo := storage.NewMemoryRepo(db)
	historyRepo := storage.NewUserHistoryRepo(db)
	memoryChunkRepo := storage.NewMemoryChunkRepo(db)

	reg := skillsys.NewRegistry()
	reg.Register(milvus_search.New())
	reg.Register(doc_ingest.New(articleRepo, infra.NewAIClient()))
	reg.Register(pool_manage.NewPoolGetSize(poolRepo))
	reg.Register(pool_manage.NewPoolRefill(poolRepo, articleRepo, reg))
	reg.Register(pool_manage.NewPoolPopTopK(poolRepo))
	reg.Register(user_history.NewAdd(historyRepo))
	reg.Register(user_history.NewRecent(historyRepo))
	reg.Register(user_history.NewSimilar(historyRepo))
	reg.Register(memory_manage.NewGet(memoryRepo))
	reg.Register(memory_manage.NewUpsert(memoryRepo, memoryChunkRepo))
	reg.Register(memory_manage.NewMaintainWindow(historyRepo, articleRepo, memoryRepo, memoryChunkRepo))
	reg.Register(rerank.New(articleRepo, memoryRepo, memoryChunkRepo))

	client := infra.NewAIClient()

	sys := "你是一个推荐系统的测试助手。你可以调用工具来完成任务。\n" +
		"要求：\n" +
		"1) 先解释你的意图理解与置信度（用 JSON 字段表达）。\n" +
		"2) 需要检索时必须调用 milvus_search / pool_refill 等工具，不允许凭空编造。\n" +
		"3) 最终输出 JSON：{intent:{label,confidence}, decision:{route,reason_codes}, tools:[...], answer:{article_ids:[...],explain:\"\"}}。\n"

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(sys),
	}

	tools := reg.OpenAITools()

	reader := bufio.NewReader(os.Stdin)
	zlog.L().Info("进入人机对话式测试：输入内容并回车；输入 exit 退出。")

	for {
		_, _ = os.Stdout.WriteString("你：")
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "exit" {
			break
		}

		messages = append(messages, openai.UserMessage(line))

		for {
			resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
				Model:       config.Cfg.Agent.Model,
				Messages:    messages,
				Tools:       tools,
				Temperature: openai.Float(0.2),
			})
			if err != nil {
				zlog.L().Error("模型调用失败", zap.Error(err))
				break
			}

			msg := resp.Choices[0].Message
			if len(msg.ToolCalls) == 0 {
				zlog.L().Info("AI 输出", zap.String("content", msg.Content))
				// 把 assistant 消息写回上下文（用 ToParam 转成入参类型）
				messages = append(messages, msg.ToParam())
				break
			}

			// 有工具调用：先把 assistant 的 tool_calls 消息写回，再逐个执行工具并回填 tool message
			messages = append(messages, msg.ToParam())

			for _, tc := range msg.ToolCalls {
				name := tc.Function.Name
				args := tc.Function.Arguments

				zlog.L().Info("工具调用", zap.String("tool", name))
				zlog.L().Info("工具入参", zap.String("args", args))

				outStr, _, err := reg.Invoke(ctx, name, json.RawMessage(args))
				if err != nil {
					zlog.L().Error("工具执行失败", zap.Error(err))
					// 仍然把错误返回给模型，便于其做降级决策
					outStr = "{\"error\":\"" + escapeJSON(err.Error()) + "\"}"
				}

				zlog.L().Info("工具出参", zap.String("out", outStr))

				messages = append(messages, openai.ToolMessage(outStr, tc.ID))
			}
		}
	}
}

func escapeJSON(s string) string {
	b, _ := json.Marshal(s)
	// b 是带引号的 JSON string
	if len(b) >= 2 {
		return string(b[1 : len(b)-1])
	}
	return s
}
