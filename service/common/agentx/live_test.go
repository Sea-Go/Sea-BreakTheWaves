package agentx

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/datacenter"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

func TestConfiguredModelActuallyReadsTool(t *testing.T) {
	if os.Getenv("BTW_TEST_DATACENTER_URL") == "" || os.Getenv("DATACENTER_WORKLOAD_TOKEN_FILE") == "" {
		t.Skip("requires isolated DataCenter with a workload identity")
	}
	c := Config{MaxLLMCalls: 3, MaxToolCalls: 2, MaxOutputTokens: 512}
	connector, e := datacenter.New(os.Getenv("BTW_TEST_DATACENTER_URL"), datacenter.DeploymentIdentity())
	if e != nil {
		t.Fatal(e)
	}
	manifest, e := connector.Manifest(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	m, e := connector.Chat(manifest.Chat.Agent, "agent")
	if e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	type input struct {
		Topic string `json:"topic"`
	}
	type output struct {
		Quote string `json:"quote"`
	}
	source := function.NewFunctionTool(func(ctx context.Context, in input) (output, error) {
		calls.Add(1)
		return output{Quote: "鞘翅目属于昆虫纲。"}, nil
	}, function.WithName("read_test_source"), function.WithDescription("Read the small test source before answering."))
	engine, e := New("btw-live-validation", m, "You must call read_test_source before answering. Return only JSON with answer and quote fields; copy the returned source quote exactly.", []tool.Tool{source}, c)
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	text, e := engine.Run(ctx, "validation-user", "请先调用工具读取测试资料，再回答鞘翅目属于什么。输出 JSON。", nil)
	if e != nil {
		t.Fatal("configured generation request failed")
	}
	var result struct {
		Answer string `json:"answer"`
		Quote  string `json:"quote"`
	}
	if Decode(text, &result) != nil || calls.Load() < 1 || result.Quote != "鞘翅目属于昆虫纲。" || !strings.Contains(result.Answer, "昆虫") {
		t.Fatal("model did not complete grounded tool contract")
	}
	t.Log("Configured model performed a real framework tool call and returned the saved source quote.")
}
