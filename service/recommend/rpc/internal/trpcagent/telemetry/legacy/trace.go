// trace.go — OTel GenAI span 抽象（Task 14.1）。
//
// 该文件提供 OpenTelemetry GenAI 语义约定的 span 抽象，避免直接 import
// otel/jaeger，通过 interface 解耦，便于离线编译与二开切换后端。
//
// 职责：
//   - 定义 Tracer/Span interface，封装 StartSpan/SetAttribute/AddEvent/
//     SetStatus/End 等链路追踪能力
//   - 提供 NoopTracer/NoopSpan 默认空实现（无后端时零开销）
//   - 实现 GenAISpan，封装 GenAI 语义属性（model/prompt/completion/tokens/
//     path/graph）的快捷设置方法
//   - 定义 TraceContext，统一承载 trace_id/span_id/path/cost 元信息
//   - 提供 ExtractTraceID 从 context 提取 trace_id
//
// 二开扩展点：
//   - 实现 Tracer/Span interface 对接真实 OTel/Jaeger 后端
//   - 通过 context 注入 trace_id（obsTraceKey{}）供跨服务传递
package obs

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// obsTraceKey context key 用于注入/提取 trace_id。
// 不与 otel trace context 冲突，作为本包薄封装层的独立 trace_id 通道。
type obsTraceKey struct{}

// SpanStatusCode span 状态码，与 OTel codes.StatusCode 语义对齐。
type SpanStatusCode int

const (
	// SpanStatusUnset 未设置状态（默认）。
	SpanStatusUnset SpanStatusCode = 0
	// SpanStatusOK 成功状态。
	SpanStatusOK SpanStatusCode = 1
	// SpanStatusError 错误状态。
	SpanStatusError SpanStatusCode = 2
)

// Tracer 链路追踪封装 interface。
//
// 二开：实现该 interface 对接 otel.Tracer / jaeger.Tracer 等真实后端。
type Tracer interface {
	// StartSpan 启动子 span，返回携带 span 的新 context 与 span 句柄。
	// attrs 为初始属性（可为空）。
	StartSpan(ctx context.Context, name string, attrs map[string]string) (context.Context, Span)
}

// Span span 句柄 interface。
//
// 二开：实现该 interface 包装 otel.Span / jaeger.Span。
type Span interface {
	// SetAttribute 设置单个属性。
	SetAttribute(key string, value any)
	// AddEvent 添加事件（可为空 attrs）。
	AddEvent(name string, attrs map[string]string)
	// SetStatus 设置状态码与描述。
	SetStatus(code SpanStatusCode, description string)
	// End 结束 span。
	End()
}

// ----------------------------------------------------------------------------
// NoopTracer / NoopSpan — 默认空实现
// ----------------------------------------------------------------------------

// NoopTracer 空实现 Tracer，不发射任何 span。
// 用于未配置真实后端时的默认值，零开销。
type NoopTracer struct{}

// StartSpan 返回原 context 与 NoopSpan。
func (NoopTracer) StartSpan(ctx context.Context, _ string, _ map[string]string) (context.Context, Span) {
	return ctx, NoopSpan{}
}

// NoopSpan 空实现 Span，所有方法均为空操作。
type NoopSpan struct{}

// SetAttribute 空操作。
func (NoopSpan) SetAttribute(_ string, _ any) {}

// AddEvent 空操作。
func (NoopSpan) AddEvent(_ string, _ map[string]string) {}

// SetStatus 空操作。
func (NoopSpan) SetStatus(_ SpanStatusCode, _ string) {}

// End 空操作。
func (NoopSpan) End() {}

// ----------------------------------------------------------------------------
// GenAISpan — GenAI 语义 span
// ----------------------------------------------------------------------------

// GenAISpan 封装 GenAI 语义属性的 span。
//
// 通过 NewGenAISpan 构造，调用 SetGenAIAttrs/SetPathAttr/SetGraphAttr
// 快捷设置 GenAI 语义属性，最后调用 End 结束 span。
//
// 字段语义：
//   - tracer：底层 Tracer（用于扩展点回查）
//   - span：底层 Span 句柄
//   - attrs：已设置的属性快照（用于测试与断言）
type GenAISpan struct {
	tracer Tracer
	span   Span
	attrs  map[string]string
}

// NewGenAISpan 构造 GenAISpan，启动底层 span 并初始化属性快照。
// tracer 底层 Tracer（可为 NoopTracer{}）；ctx 父 context；
// name span 名称（如 "recommend.fast_path"）。
func NewGenAISpan(tracer Tracer, ctx context.Context, name string) *GenAISpan {
	if tracer == nil {
		tracer = NoopTracer{}
	}
	_, span := tracer.StartSpan(ctx, name, nil)
	return &GenAISpan{
		tracer: tracer,
		span:   span,
		attrs:  make(map[string]string),
	}
}

// SetGenAIAttrs 设置 GenAI 语义属性。
// model 模型名；prompt 输入提示；completion 模型响应；
// inputTokens 输入 token 数；outputTokens 输出 token 数；cachedTokens 命中缓存 token 数。
func (s *GenAISpan) SetGenAIAttrs(model, prompt, completion string, inputTokens, outputTokens, cachedTokens int) {
	if s == nil || s.span == nil {
		return
	}
	s.span.SetAttribute("gen_ai.system", "trpc-agent-go")
	s.span.SetAttribute("gen_ai.request.model", model)
	s.span.SetAttribute("gen_ai.prompt", prompt)
	s.span.SetAttribute("gen_ai.completion", completion)
	s.span.SetAttribute("gen_ai.usage.input_tokens", inputTokens)
	s.span.SetAttribute("gen_ai.usage.output_tokens", outputTokens)
	s.span.SetAttribute("gen_ai.usage.cached_tokens", cachedTokens)
	s.attrs["gen_ai.request.model"] = model
	s.attrs["gen_ai.usage.input_tokens"] = itoa(inputTokens)
	s.attrs["gen_ai.usage.output_tokens"] = itoa(outputTokens)
	s.attrs["gen_ai.usage.cached_tokens"] = itoa(cachedTokens)
}

// SetPathAttr 设置推荐路径属性（fast/slow/hybrid）。
func (s *GenAISpan) SetPathAttr(path string) {
	if s == nil || s.span == nil {
		return
	}
	s.span.SetAttribute("genrec.path", path)
	s.attrs["genrec.path"] = path
}

// SetGraphAttr 设置图谱查询属性。
// cypher 执行的 Cypher 语句；nodeCount 返回节点数；edgeCount 返回边数。
func (s *GenAISpan) SetGraphAttr(cypher string, nodeCount, edgeCount int) {
	if s == nil || s.span == nil {
		return
	}
	s.span.SetAttribute("genrec.graph.cypher", cypher)
	s.span.SetAttribute("genrec.graph.node_count", nodeCount)
	s.span.SetAttribute("genrec.graph.edge_count", edgeCount)
	s.attrs["genrec.graph.cypher"] = cypher
	s.attrs["genrec.graph.node_count"] = itoa(nodeCount)
	s.attrs["genrec.graph.edge_count"] = itoa(edgeCount)
}

// End 结束 span。
func (s *GenAISpan) End() {
	if s == nil || s.span == nil {
		return
	}
	s.span.End()
}

// Attrs 返回已设置属性快照（测试与断言用，返回副本避免外部修改）。
func (s *GenAISpan) Attrs() map[string]string {
	if s == nil {
		return nil
	}
	out := make(map[string]string, len(s.attrs))
	for k, v := range s.attrs {
		out[k] = v
	}
	return out
}

// ----------------------------------------------------------------------------
// TraceContext
// ----------------------------------------------------------------------------

// TraceContext 统一承载链路元信息，用于跨方法/跨服务传递 trace 上下文。
//
// 字段语义：
//   - TraceID：链路 ID（对应 OTel trace_id）
//   - SpanID：当前 span ID
//   - PathTaken：推荐路径（fast/slow/hybrid）
//   - Cost：成本报告
type TraceContext struct {
	TraceID   string
	SpanID    string
	PathTaken string
	Cost      domain.CostReport
}

// ExtractTraceID 从 context 提取 trace_id。
// 通过 obsTraceKey{} 注入的 trace_id 优先返回；未注入返回空串。
func ExtractTraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, ok := ctx.Value(obsTraceKey{}).(string)
	if !ok {
		return ""
	}
	return v
}

// WithTraceID 将 trace_id 注入 context，返回新 context。
// 供上游设置 trace_id 后跨方法传递。
func WithTraceID(ctx context.Context, traceID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, obsTraceKey{}, traceID)
}

// itoa 简易 int -> string 转换，避免引入 strconv（薄封装层尽量零依赖）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
