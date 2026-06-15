package amap

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agent_v3/internal/config"
)

func TestAmapAgentReasoningCapture(t *testing.T) {
	if err := config.Load("../config.yaml"); err != nil {
		t.Skipf("跳过：无法加载 config.yaml: %v", err)
	}

	if config.Cfg.Ali.ApiKey == "" || len(config.Cfg.Ali.HighModels) == 0 {
		t.Skip("跳过：Ali API Key 或 HighModels 未配置")
	}

	userID := "test-reasoning-user"
	sessionID := "test-reasoning-session"
	userMessage := "北京天安门附近的咖啡店有哪些？"

	eventCh, cancel, err := AmapAgentRun(userID, sessionID, userMessage)
	if err != nil {
		t.Fatalf("AmapAgentRun 失败: %v", err)
	}

	var (
		answerBuf    strings.Builder
		reasoningBuf strings.Builder
		toolCallsBuf strings.Builder
		firstTool    = true
		gotContent   bool
		gotReasoning bool
		gotToolCalls bool
	)

	timeout := time.After(120 * time.Second)

loop:
	for {
		select {
		case <-timeout:
			t.Fatal("超时：120 秒内未完成")
		case evt, ok := <-eventCh:
			if !ok {
				break loop
			}
			if evt == nil || evt.Response == nil {
				continue
			}
			for _, choice := range evt.Response.Choices {
				if choice.Delta.Content != "" {
					answerBuf.WriteString(choice.Delta.Content)
					gotContent = true
				} else if choice.Message.Content != "" && answerBuf.Len() == 0 {
					answerBuf.WriteString(choice.Message.Content)
					gotContent = true
				}

				if choice.Delta.ReasoningContent != "" {
					reasoningBuf.WriteString(choice.Delta.ReasoningContent)
					gotReasoning = true
				} else if choice.Message.ReasoningContent != "" {
					reasoningBuf.WriteString(choice.Message.ReasoningContent)
					gotReasoning = true
				}

				for _, tc := range choice.Message.ToolCalls {
					if !firstTool {
						toolCallsBuf.WriteString("\n")
					}
					firstTool = false
					gotToolCalls = true
					tcJSON, _ := json.Marshal(tc)
					toolCallsBuf.Write(tcJSON)
				}
				for _, tc := range choice.Delta.ToolCalls {
					if !firstTool {
						toolCallsBuf.WriteString("\n")
					}
					firstTool = false
					gotToolCalls = true
					tcJSON, _ := json.Marshal(tc)
					toolCallsBuf.Write(tcJSON)
				}
			}
		}
	}

	cancel()

	t.Logf("=== 最终回答 ===\n%s", answerBuf.String())
	t.Logf("=== 思考过程 ===\n%s", reasoningBuf.String())
	t.Logf("=== 工具调用 ===\n%s", toolCallsBuf.String())

	t.Logf("\n--- 检查结果 ---")
	t.Logf("gotContent:   %v (len=%d)", gotContent, answerBuf.Len())
	t.Logf("gotReasoning: %v (len=%d)", gotReasoning, reasoningBuf.Len())
	t.Logf("gotToolCalls: %v (len=%d)", gotToolCalls, toolCallsBuf.Len())

	if !gotContent {
		t.Error("未获取到任何回答内容")
	}
	if !gotReasoning {
		t.Error("未获取到思考过程 (ReasoningContent) —— planner 模式可能未生效或模型不支持")
	}
	if !gotToolCalls {
		t.Error("未检测到工具调用 —— Agent 可能未调用高德工具")
	}
}
