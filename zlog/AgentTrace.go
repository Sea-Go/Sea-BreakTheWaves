package zlog

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	types "sea/type"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

//
// =====================================================================================
// ✅ 服务侧“真正用于打日志”的核心入口（建议从这里开始看）
// =====================================================================================
//
// 你在服务里最常用的调用顺序通常是：
//   ctx = NewTrace(...)                     // 入口创建 trace（把 trace_id / rec_request_id 等塞进 ctx）
//   LogRootInvoke(ctx, ...)                 // 打 root 事件：invoke_agent（一次 agent 请求开始）
//
//   ctx1, sp := StartSpan(ctx, "intent...") // 每个 step 开始一个 span
//   sp.End(StatusOK, nil, zap.Any(...))     // step 结束，输出一条结构化日志（带 latency_ms）
//   ...                                     // 下一个 step 重复 StartSpan + End
//

// LogRootInvoke 打印一次 agent 调用的「根事件」日志（event_type=invoke_agent）。
//
// ✅ 用于：标记一次请求开始（root span），便于从日志/trace 中快速定位“这一条请求”的入口。
// ✅ 输出字段（核心）：
//   - ts, event_type=invoke_agent
//   - trace_id, rec_request_id, user_id_hash, session_id, surface, exp_ids, agent{name}
//   - span_id=0000, parent_span_id=null
//   - status, error
func LogRootInvoke(ctx context.Context, status Status, err error, surface string) {
	lg := L().WithOptions(zap.AddCallerSkip(1))
	all := append(baseZapFields(ctx),
		zap.String("ts", time.Now().UTC().Format(time.RFC3339Nano)),
		zap.String("event_type", "invoke_agent"),
		zap.String("span_id", "0000"),
		zap.Any("parent_span_id", nil),
		zap.String("status", strconv.Itoa(int(status))),
		zap.Any("error", errToObj(err)),
		zap.String("surface", surface),
	)
	logAgentEvent(lg, status, all...)
}

// StartSpan 开始一个 step span（不会立刻打印日志）。
//
// ✅ 用于：每个 agent step（intent/policy/retrieval/tool/chat/validate/guardrail...）开始时调用。
// ✅ 做的事：
//   - 从 ctx 里取当前 span_id 作为 parent_span_id
//   - 生成一个新的 span_id（短 ID）
//   - 返回更新后的 ctx（携带新的 span_id）和 *Span
//
// ⚠️ 注意：StartSpan 本身不打印日志；真正打印在 sp.End(...)
func StartSpan(ctx context.Context, eventType string) (context.Context, *Span) {
	b, ok := BaseFrom(ctx)
	parent := ""
	if ok {
		parent = b.SpanID
	}

	// 生成一个短 span_id（用于日志串联；OTel 也会有自己的 SpanID）
	spanID := shortSpanID()

	// 让 Jaeger 能看到每个 step：这里同步创建一个 OTel span
	tr := otel.Tracer("sea/agent")
	otelCtx, otelSpan := tr.Start(ctx, eventType)

	childCtx := UpdateSpan(otelCtx, spanID)

	return childCtx, &Span{
		ctx:          childCtx,
		eventType:    eventType,
		spanID:       spanID,
		parentSpanID: parent,
		start:        time.Now(),
		otelSpan:     otelSpan,
	}
}

// End 结束一个 step span 并打印一条结构化日志（核心方法）。
//
// ✅ 用于：每个 step 结束时调用，输出该 step 的“结构化事件”日志。
// ✅ 输出字段（核心）：
//   - ts, event_type（你传入的 eventType，例如 intent.inferred / policy.routed / retrieval.completed ...）
//   - trace_id, rec_request_id, user_id_hash, session_id, surface, exp_ids, agent{name}
//   - span_id, parent_span_id（自动拼链）
//   - latency_ms（自动计算 step 耗时）
//   - status（OK/ERROR/DEGRADED/FALLBACK）
//   - error（包含 message + class）
//
// ✅ 你额外传入的 fields（zap.Any("decision", ...), zap.Any("retrieval", ...) 等）会一起落日志
func (s *Span) End(status Status, err error, fields ...zap.Field) {
	lat := time.Since(s.start)

	// OTel：补充 span 状态/错误（Jaeger 能看到）
	if s.otelSpan != nil {
		s.otelSpan.SetAttributes(
			// 这里避免写高基数：只落关键摘要
			attribute.Int64("latency_ms", lat.Milliseconds()),
		)
		if err != nil {
			s.otelSpan.RecordError(err)
			s.otelSpan.SetStatus(codes.Error, err.Error())
		} else {
			s.otelSpan.SetStatus(codes.Ok, "")
		}
		defer s.otelSpan.End()
	}

	// 确保 caller 指到业务层，而不是 wrapper
	lg := L().WithOptions(zap.AddCallerSkip(1))
	all := append(baseZapFields(s.ctx), spanFields(s.spanID, s.parentSpanID)...)

	all = append(all,
		zap.String("ts", time.Now().UTC().Format(time.RFC3339Nano)),
		zap.String("event_type", s.eventType),
		zap.Int64("latency_ms", lat.Milliseconds()),
		zap.String("status", strconv.Itoa(int(status))),
	)

	if err != nil {
		all = append(all, zap.Any("error", map[string]any{
			"message": err.Error(),
			"class":   ErrorClass(err),
		}))
	} else {
		all = append(all, zap.Any("error", nil))
	}

	all = append(all, fields...)
	logAgentEvent(lg, status, all...)
}

// NewTrace 创建一次请求的 trace 上下文（不会立刻打印日志）。
//
// ✅ 用于：请求入口处构造“贯穿全链路”的 ctx（trace_id / rec_request_id 等）。后续 StartSpan/End 会从 ctx 里取这些字段打印。
// ✅ 做的事：
//   - 生成 trace_id
//   - 初始化 root span_id=0000
//   - 对 userID 做 hash（避免明文高基数/隐私风险）
//   - 保存 surface/agent/exp_ids/session_id 等低基数字段
func NewTrace(ctx context.Context, recRequestID, surface, agentName, userID, sessionID string, expIDs []ExpID) context.Context {
	// 如果上游已经创建了 OTel span（例如 HTTP 中间件），优先使用其 trace_id，
	// 这样日志的 trace_id 可以和 Jaeger 中的 trace 对齐。
	traceID := ""
	if sp := trace.SpanFromContext(ctx); sp != nil {
		sc := sp.SpanContext()
		if sc.IsValid() {
			traceID = sc.TraceID().String()
		}
	}
	if traceID == "" {
		traceID = randHex(16) // 32 hex chars
	}

	b := BaseContext{
		TraceID:      traceID,
		SpanID:       "0000",
		RecRequestID: recRequestID,
		UserIDHash:   HashID(userID),
		SessionID:    sessionID,
		Surface:      surface,
		ExpIDs:       expIDs,
		AgentName:    agentName,
	}
	return WithBase(ctx, b)
}

//
// =====================================================================================
// 下面是：上下文字段/结构体 schema/辅助函数（服务一般“间接使用”）
// =====================================================================================

// Status：请求/step 的结果状态（用于日志 status 字段）
type Status = types.Status

const (
	StatusOK       Status = types.StatusOK
	StatusDegraded Status = types.StatusDegraded
	StatusFallback Status = types.StatusFallback
	StatusError    Status = types.StatusError
)

type ExpID = types.ExpID

type BaseContext = types.BaseContext

type ctxKeyBase struct{}

// WithBase：把 BaseContext 放进 context，用于跨 goroutine/队列传播
func WithBase(ctx context.Context, b BaseContext) context.Context {
	return context.WithValue(ctx, ctxKeyBase{}, b)
}

// BaseFrom：从 context 取出 BaseContext
func BaseFrom(ctx context.Context) (BaseContext, bool) {
	v := ctx.Value(ctxKeyBase{})
	if v == nil {
		return BaseContext{}, false
	}
	b, ok := v.(BaseContext)
	return b, ok
}

// UpdateSpan：更新 ctx 中的 span_id（StartSpan 内部会用，业务一般不用手动调）
func UpdateSpan(ctx context.Context, spanID string) context.Context {
	b, ok := BaseFrom(ctx)
	if !ok {
		return ctx
	}
	b.SpanID = spanID
	return WithBase(ctx, b)
}

type Span struct {
	ctx          context.Context
	eventType    string
	spanID       string
	parentSpanID string
	start        time.Time

	// otelSpan 用于把链路同步到 Jaeger（OpenTelemetry Trace）
	otelSpan trace.Span
}

func spanFields(spanID, parentSpanID string) []zap.Field {
	if parentSpanID == "" {
		return []zap.Field{
			zap.String("span_id", spanID),
			zap.Any("parent_span_id", nil),
		}
	}
	return []zap.Field{
		zap.String("span_id", spanID),
		zap.String("parent_span_id", parentSpanID),
	}
}

// baseZapFields：从 ctx 提取“整条链路共用字段”，每条 step 日志都会带上
func baseZapFields(ctx context.Context) []zap.Field {
	b, ok := BaseFrom(ctx)
	if !ok {
		return nil
	}

	fs := []zap.Field{
		zap.String("trace_id", b.TraceID),
		zap.String("rec_request_id", b.RecRequestID),
	}

	// 额外：把 OTel 的 trace/span 也写进日志，便于从日志直接跳到 Jaeger
	if sp := trace.SpanFromContext(ctx); sp != nil {
		sc := sp.SpanContext()
		if sc.IsValid() {
			fs = append(fs,
				zap.String("otel_trace_id", sc.TraceID().String()),
				zap.String("otel_span_id", sc.SpanID().String()),
			)
		}
	}

	if b.UserIDHash != "" {
		fs = append(fs, zap.String("user_id_hash", b.UserIDHash))
	}
	if b.SessionID != "" {
		fs = append(fs, zap.String("session_id", b.SessionID))
	}
	if b.Surface != "" {
		fs = append(fs, zap.String("surface", b.Surface))
	}
	if b.AgentName != "" {
		// 日志中写为 agent:{name:"..."}，与示例保持一致（低基数）
		fs = append(fs, zap.Any("agent", map[string]string{"name": b.AgentName}))
	}
	if len(b.ExpIDs) > 0 {
		fs = append(fs, zap.Any("exp_ids", b.ExpIDs))
	}
	if b.Locale != "" {
		fs = append(fs, zap.String("locale", b.Locale))
	}
	if b.UserTier != "" {
		fs = append(fs, zap.String("user_tier", b.UserTier))
	}
	if b.TimeSkewDetected != nil {
		fs = append(fs, zap.Bool("time_skew_detected", *b.TimeSkewDetected))
	}
	return fs
}

//
// ===== step 产物 schema：你会在 End(...) 里 zap.Any("intent"/"decision"/...) 用到 =====
//

type ModelInfo = types.ModelInfo

type Intent = types.Intent

type Decision = types.Decision

type Retrieval = types.Retrieval

type ToolCall = types.ToolCall

type Gen = types.Gen

type Quality = types.Quality

//
// ===== 一些工具函数：脱敏/错误归类/大字段引用（服务通常“间接用”）=====
//

var hashKey []byte // 建议通过 SetHashKey 注入（比如从 env 读）

// SetHashKey 设置 hash key（推荐：用于 HMAC，避免可逆/彩虹表）
func SetHashKey(key []byte) { hashKey = key }

// HashID：对 user/doc/url 等做短 hash（避免明文高基数）
func HashID(raw string) string {
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	if len(hashKey) > 0 {
		m := hmac.New(sha256.New, hashKey)
		_, _ = m.Write([]byte(raw))
		copy(sum[:], m.Sum(nil))
	}
	return "h_" + hex.EncodeToString(sum[:])[:6]
}

// ArtifactWriter：你们接入 blob/s3/oss 后实现 Put()，返回 blob://... 引用
type ArtifactWriter interface {
	Put(ctx context.Context, key string, payload []byte) (ref string, err error)
}

func errToObj(err error) any {
	if err == nil {
		return nil
	}
	return map[string]any{
		"message": err.Error(),
		"class":   ErrorClass(err),
	}
}

// ErrorClass：把 error 粗分桶（避免把长 error message 当指标/维度）
func ErrorClass(err error) string {
	if err == nil {
		return "NONE"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timeout"):
		return "TIMEOUT"
	case strings.Contains(msg, "rate limit"), strings.Contains(msg, "too many requests"):
		return "RATE_LIMIT"
	case strings.Contains(msg, "invalid"), strings.Contains(msg, "bad request"):
		return "INVALID_ARGS"
	case strings.Contains(msg, "5xx"), strings.Contains(msg, "internal server"):
		return "UPSTREAM_5XX"
	default:
		return "UNKNOWN"
	}
}

func ConfidenceBucket(x float64) string {
	if x < 0 {
		x = 0
	}
	if x > 1 {
		x = 1
	}
	i := int(x * 10)
	if i == 10 {
		i = 9
	}
	low := float64(i) / 10.0
	high := low + 0.1
	return trimFloat(low) + "-" + trimFloat(high)
}

func trimFloat(f float64) string {
	s := strings.TrimRight(strings.TrimRight(
		strings.TrimSpace(
			strings.TrimRight(
				strings.TrimRight(
					strings.TrimSpace(
						strings.TrimRight(
							strings.TrimRight(
								strings.TrimSpace(""), "0"),
							".")),
					"0"),
				".")),
		"0"),
		".")
	_ = s
	return strings.TrimRight(strings.TrimRight(
		strings.TrimSpace(
			strings.ReplaceAll(
				strings.ReplaceAll(
					strings.ReplaceAll(
						strings.ReplaceAll(
							strings.TrimSpace(
								strings.TrimSpace(
									strings.TrimSpace(""),
								),
							),
							"\n", ""),
						"\t", ""),
					"\r", ""),
				"\f", ""),
		), "0"), ".")
}

func randHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func shortSpanID() string {
	return randHex(2)
}

func logAgentEvent(lg *zap.Logger, status Status, fields ...zap.Field) {
	switch status {
	case StatusError:
		lg.Error("agent_event", fields...)
	case StatusDegraded, StatusFallback:
		lg.Warn("agent_event", fields...)
	default:
		lg.Info("agent_event", fields...)
	}
}
