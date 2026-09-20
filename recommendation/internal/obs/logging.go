// logging.go — 结构化 JSON 日志（Task 14.1）。
//
// 该文件提供结构化 JSON 日志器，避免直接依赖 zap/logrus，通过自实现
// 满足离线编译与薄封装需求。日志携带 OTel trace_id，可按 trace_id/
// channel/user_id 检索。
//
// 职责：
//   - 定义 LogLevel（DEBUG/INFO/WARN/ERROR）
//   - 实现 StructuredLogger，支持 WithField/WithFields/WithTraceID 链式
//   - 输出 JSON 日志到 stdout：{"timestamp","level","msg","trace_id","fields"}
//   - 提供 LogAgentStep/LogPathDecision/LogGraphQuery 业务快捷日志
//
// 二开扩展点：
//   - 替换 logOutput（io.Writer）对接 ES/Filebeat/本地文件
//   - 通过 WithField 注入业务字段（channel/user_id/agent_name）
package obs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// LogLevel 日志级别。
type LogLevel int

const (
	// LogLevelDebug DEBUG 级别（最低）。
	LogLevelDebug LogLevel = 0
	// LogLevelInfo INFO 级别。
	LogLevelInfo LogLevel = 1
	// LogLevelWarn WARN 级别。
	LogLevelWarn LogLevel = 2
	// LogLevelError ERROR 级别（最高）。
	LogLevelError LogLevel = 3
)

// logLevelName 日志级别名称映射。
var logLevelName = map[LogLevel]string{
	LogLevelDebug: "DEBUG",
	LogLevelInfo:  "INFO",
	LogLevelWarn:  "WARN",
	LogLevelError: "ERROR",
}

// logEntry JSON 日志条目结构。
//
// 字段语义：
//   - Timestamp：RFC3339 时间戳
//   - Level：日志级别名
//   - Msg：日志消息
//   - TraceID：链路 ID（可为空）
//   - Fields：业务字段（可为空）
type logEntry struct {
	Timestamp string         `json:"timestamp"`
	Level     string         `json:"level"`
	Msg       string         `json:"msg"`
	TraceID   string         `json:"trace_id,omitempty"`
	Fields    map[string]any `json:"fields,omitempty"`
}

// StructuredLogger 结构化 JSON 日志器。
//
// 字段语义：
//   - level：最低输出级别（低于该级别的日志被丢弃）
//   - fields：预置业务字段（通过 WithField/WithFields 累积）
//   - traceID：预置 trace_id（通过 WithTraceID 设置或从 context 提取）
//   - out：输出目标（默认 os.Stdout，可替换为 *os.File 等实现 io.Writer）
//   - mu：保护 out 写入串行化
type StructuredLogger struct {
	level   LogLevel
	fields  map[string]any
	traceID string
	out     *os.File
	mu      sync.RWMutex
}

// NewStructuredLogger 构造 StructuredLogger。
// level 最低输出级别；输出默认到 os.Stdout。
func NewStructuredLogger(level LogLevel) *StructuredLogger {
	return &StructuredLogger{
		level: level,
		out:   os.Stdout,
	}
}

// WithField 链式添加单个字段，返回新 logger（不修改原 logger）。
func (l *StructuredLogger) WithField(key string, value any) *StructuredLogger {
	if l == nil {
		return nil
	}
	nl := l.clone()
	if nl.fields == nil {
		nl.fields = make(map[string]any)
	}
	nl.fields[key] = value
	return nl
}

// WithFields 链式添加多个字段，返回新 logger。
func (l *StructuredLogger) WithFields(fields map[string]any) *StructuredLogger {
	if l == nil {
		return nil
	}
	nl := l.clone()
	if len(fields) > 0 {
		if nl.fields == nil {
			nl.fields = make(map[string]any)
		}
		for k, v := range fields {
			nl.fields[k] = v
		}
	}
	return nl
}

// WithTraceID 链式设置 trace_id，返回新 logger。
func (l *StructuredLogger) WithTraceID(traceID string) *StructuredLogger {
	if l == nil {
		return nil
	}
	nl := l.clone()
	nl.traceID = traceID
	return nl
}

// Info 输出 INFO 级别日志。
func (l *StructuredLogger) Info(msg string) {
	l.log(LogLevelInfo, msg)
}

// Warn 输出 WARN 级别日志。
func (l *StructuredLogger) Warn(msg string) {
	l.log(LogLevelWarn, msg)
}

// Error 输出 ERROR 级别日志。
func (l *StructuredLogger) Error(msg string) {
	l.log(LogLevelError, msg)
}

// Debug 输出 DEBUG 级别日志。
func (l *StructuredLogger) Debug(msg string) {
	l.log(LogLevelDebug, msg)
}

// LogAgentStep 记录 Agent 步骤日志。
// agentName Agent 名称；step 步骤名（如 "tool_call"/"llm_call"）；
// attrs 额外属性。
func (l *StructuredLogger) LogAgentStep(ctx context.Context, agentName, step string, attrs map[string]any) {
	if l == nil {
		return
	}
	fields := map[string]any{
		"agent_name": agentName,
		"step":       step,
	}
	for k, v := range attrs {
		fields[k] = v
	}
	nl := l.WithFields(fields)
	nl.applyTraceFromCtx(ctx)
	nl.Info("agent_step")
}

// LogPathDecision 记录路径决策日志。
// path 推荐路径（fast/slow/hybrid）；complexity 复杂度分。
func (l *StructuredLogger) LogPathDecision(ctx context.Context, path string, complexity float64) {
	if l == nil {
		return
	}
	nl := l.WithFields(map[string]any{
		"path":       path,
		"complexity": complexity,
	})
	nl.applyTraceFromCtx(ctx)
	nl.Info("path_decision")
}

// LogGraphQuery 记录图谱查询日志。
// cypher 执行的 Cypher 语句；latencyMs 耗时（毫秒）；nodeCount 返回节点数。
func (l *StructuredLogger) LogGraphQuery(ctx context.Context, cypher string, latencyMs int64, nodeCount int) {
	if l == nil {
		return
	}
	nl := l.WithFields(map[string]any{
		"cypher":      cypher,
		"latency_ms":  latencyMs,
		"node_count":  nodeCount,
	})
	nl.applyTraceFromCtx(ctx)
	nl.Info("graph_query")
}

// Level 返回当前最低输出级别。
func (l *StructuredLogger) Level() LogLevel {
	if l == nil {
		return LogLevelInfo
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.level
}

// SetOutput 设置输出目标（测试用，可替换为 *os.File 指向临时文件）。
func (l *StructuredLogger) SetOutput(out *os.File) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.out = out
	l.mu.Unlock()
}

// ----------------------------------------------------------------------------
// 内部方法
// ----------------------------------------------------------------------------

// clone 复制 logger（共享 out，复制 fields/traceID）。
func (l *StructuredLogger) clone() *StructuredLogger {
	nl := &StructuredLogger{
		level:   l.level,
		traceID: l.traceID,
		out:     l.out,
		fields:  make(map[string]any, len(l.fields)),
	}
	for k, v := range l.fields {
		nl.fields[k] = v
	}
	return nl
}

// applyTraceFromCtx 若当前 logger 未设置 trace_id，则从 context 提取。
func (l *StructuredLogger) applyTraceFromCtx(ctx context.Context) {
	if l == nil || l.traceID != "" {
		return
	}
	if id := ExtractTraceID(ctx); id != "" {
		l.traceID = id
	}
}

// log 输出日志（低于 level 的丢弃）。
func (l *StructuredLogger) log(level LogLevel, msg string) {
	if l == nil {
		return
	}
	l.mu.RLock()
	if level < l.level {
		l.mu.RUnlock()
		return
	}
	out := l.out
	traceID := l.traceID
	fields := l.fields
	l.mu.RUnlock()

	entry := logEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Level:     logLevelName[level],
		Msg:       msg,
		TraceID:   traceID,
		Fields:    fields,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		// marshal 失败时降级输出明文，避免日志丢失
		fmt.Fprintf(out, `{"level":"ERROR","msg":"log marshal failed","err":%q}`+"\n", err.Error())
		return
	}
	// 串行化写入避免多协程交错
	l.mu.Lock()
	out.Write(data)
	out.Write([]byte("\n"))
	l.mu.Unlock()
}
