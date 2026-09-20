// logging_test.go — logging.go 单测（Task 14.1）。
//
// 测试覆盖：
//   - 日志格式（JSON 字段：timestamp/level/msg/trace_id/fields）
//   - 字段链式（WithField/WithFields 不修改原 logger）
//   - trace_id 设置（WithTraceID + 从 context 提取）
//   - 日志级别过滤（DEBUG/INFO/WARN/ERROR）
//   - Agent 步骤日志（LogAgentStep）
//   - 路径决策日志（LogPathDecision）
//   - 图谱查询日志（LogGraphQuery）
//
// stub 类型用 OBS 前缀避免与 callbacks_test.go 冲突。
package obs

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// captureLoggerOutput 将 logger 输出重定向到 pipe，返回 logger 与读取函数。
// 调用方在使用完毕后应调用 closeFn 关闭写端并读取剩余内容。
func captureLoggerOutput(t *testing.T, level LogLevel) (*StructuredLogger, func() string, func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe error: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	logger := NewStructuredLogger(level)
	logger.SetOutput(w)

	closeFn := func() {
		w.Close()
		os.Stdout = origStdout
	}
	readFn := func() string {
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 1024)
		for {
			n, err := r.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
			}
			if err != nil {
				break
			}
		}
		return string(buf)
	}
	return logger, readFn, closeFn
}

// parseLogLines 解析日志输出为 logEntry 列表。
func parseLogLines(t *testing.T, out string) []logEntry {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var entries []logEntry
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e logEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("failed to parse log line %q: %v", line, err)
		}
		entries = append(entries, e)
	}
	return entries
}

// ----------------------------------------------------------------------------
// 构造与级别测试
// ----------------------------------------------------------------------------

func TestNewStructuredLogger_Default(t *testing.T) {
	l := NewStructuredLogger(LogLevelInfo)
	if l == nil {
		t.Fatal("logger should not be nil")
	}
	if l.Level() != LogLevelInfo {
		t.Fatalf("Level = %v, want INFO", l.Level())
	}
}

func TestLogLevel_Constants(t *testing.T) {
	if LogLevelDebug != 0 {
		t.Fatalf("DEBUG = %d, want 0", LogLevelDebug)
	}
	if LogLevelInfo != 1 {
		t.Fatalf("INFO = %d, want 1", LogLevelInfo)
	}
	if LogLevelWarn != 2 {
		t.Fatalf("WARN = %d, want 2", LogLevelWarn)
	}
	if LogLevelError != 3 {
		t.Fatalf("ERROR = %d, want 3", LogLevelError)
	}
}

// ----------------------------------------------------------------------------
// 日志格式测试
// ----------------------------------------------------------------------------

func TestStructuredLogger_Info_Format(t *testing.T) {
	logger, readFn, closeFn := captureLoggerOutput(t, LogLevelInfo)
	defer closeFn()
	logger.Info("hello world")
	closeFn()
	out := readFn()
	entries := parseLogLines(t, out)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Level != "INFO" {
		t.Fatalf("Level = %q, want INFO", e.Level)
	}
	if e.Msg != "hello world" {
		t.Fatalf("Msg = %q, want hello world", e.Msg)
	}
	if e.Timestamp == "" {
		t.Fatal("Timestamp should not be empty")
	}
}

func TestStructuredLogger_LevelFiltering(t *testing.T) {
	logger, readFn, closeFn := captureLoggerOutput(t, LogLevelWarn)
	defer closeFn()
	logger.Debug("debug msg")
	logger.Info("info msg")
	logger.Warn("warn msg")
	logger.Error("error msg")
	closeFn()
	entries := parseLogLines(t, readFn())
	// WARN 级别应过滤掉 DEBUG 与 INFO
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (WARN+ERROR)", len(entries))
	}
	if entries[0].Level != "WARN" {
		t.Fatalf("entries[0].Level = %q, want WARN", entries[0].Level)
	}
	if entries[1].Level != "ERROR" {
		t.Fatalf("entries[1].Level = %q, want ERROR", entries[1].Level)
	}
}

// ----------------------------------------------------------------------------
// WithField / WithFields 测试
// ----------------------------------------------------------------------------

func TestStructuredLogger_WithField_Chained(t *testing.T) {
	logger, readFn, closeFn := captureLoggerOutput(t, LogLevelInfo)
	defer closeFn()
	nl := logger.WithField("channel", "tech")
	nl.Info("test msg")
	closeFn()
	entries := parseLogLines(t, readFn())
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Fields["channel"] != "tech" {
		t.Fatalf("channel = %v, want tech", entries[0].Fields["channel"])
	}
}

func TestStructuredLogger_WithField_DoesNotModifyOriginal(t *testing.T) {
	logger, readFn, closeFn := captureLoggerOutput(t, LogLevelInfo)
	defer closeFn()
	original := logger.WithField("channel", "tech")
	derived := original.WithField("user_id", "u123")
	original.Info("original msg")
	derived.Info("derived msg")
	closeFn()
	entries := parseLogLines(t, readFn())
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	// original 不应有 user_id
	if _, ok := entries[0].Fields["user_id"]; ok {
		t.Fatal("original logger should not have user_id")
	}
	// derived 应有 channel 与 user_id
	if entries[1].Fields["channel"] != "tech" {
		t.Fatalf("derived channel = %v, want tech", entries[1].Fields["channel"])
	}
	if entries[1].Fields["user_id"] != "u123" {
		t.Fatalf("derived user_id = %v, want u123", entries[1].Fields["user_id"])
	}
}

func TestStructuredLogger_WithFields_Chained(t *testing.T) {
	logger, readFn, closeFn := captureLoggerOutput(t, LogLevelInfo)
	defer closeFn()
	nl := logger.WithFields(map[string]any{
		"channel": "tech",
		"user_id": "u123",
	})
	nl.Info("test msg")
	closeFn()
	entries := parseLogLines(t, readFn())
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Fields["channel"] != "tech" {
		t.Fatalf("channel = %v", entries[0].Fields["channel"])
	}
	if entries[0].Fields["user_id"] != "u123" {
		t.Fatalf("user_id = %v", entries[0].Fields["user_id"])
	}
}

func TestStructuredLogger_WithFields_NilFields_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("WithFields(nil) panicked: %v", r)
		}
	}()
	logger := NewStructuredLogger(LogLevelInfo)
	nl := logger.WithFields(nil)
	nl.Info("test")
}

func TestStructuredLogger_WithField_NilReceiver_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil receiver panicked: %v", r)
		}
	}()
	var l *StructuredLogger
	if nl := l.WithField("k", "v"); nl != nil {
		t.Fatal("nil receiver WithField should return nil")
	}
	if nl := l.WithFields(nil); nl != nil {
		t.Fatal("nil receiver WithFields should return nil")
	}
	if nl := l.WithTraceID("t"); nl != nil {
		t.Fatal("nil receiver WithTraceID should return nil")
	}
	l.Info("test")
	l.Warn("test")
	l.Error("test")
	l.Debug("test")
}

// ----------------------------------------------------------------------------
// WithTraceID 测试
// ----------------------------------------------------------------------------

func TestStructuredLogger_WithTraceID(t *testing.T) {
	logger, readFn, closeFn := captureLoggerOutput(t, LogLevelInfo)
	defer closeFn()
	nl := logger.WithTraceID("trace-abc-123")
	nl.Info("test msg")
	closeFn()
	entries := parseLogLines(t, readFn())
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].TraceID != "trace-abc-123" {
		t.Fatalf("TraceID = %q, want trace-abc-123", entries[0].TraceID)
	}
}

func TestStructuredLogger_WithTraceID_DoesNotModifyOriginal(t *testing.T) {
	logger, readFn, closeFn := captureLoggerOutput(t, LogLevelInfo)
	defer closeFn()
	original := logger.WithTraceID("trace-orig")
	derived := original.WithField("k", "v")
	original.Info("original")
	derived.Info("derived")
	closeFn()
	entries := parseLogLines(t, readFn())
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if entries[0].TraceID != "trace-orig" {
		t.Fatalf("original TraceID = %q, want trace-orig", entries[0].TraceID)
	}
	if entries[1].TraceID != "trace-orig" {
		t.Fatalf("derived TraceID = %q, want trace-orig (inherited)", entries[1].TraceID)
	}
}

// ----------------------------------------------------------------------------
// LogAgentStep 测试
// ----------------------------------------------------------------------------

func TestStructuredLogger_LogAgentStep(t *testing.T) {
	logger, readFn, closeFn := captureLoggerOutput(t, LogLevelInfo)
	defer closeFn()
	ctx := WithTraceID(context.Background(), "trace-step-1")
	logger.LogAgentStep(ctx, "OrchestratorAgent", "tool_call", map[string]any{
		"tool":   "recall.content",
		"status": "ok",
	})
	closeFn()
	entries := parseLogLines(t, readFn())
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Msg != "agent_step" {
		t.Fatalf("Msg = %q, want agent_step", e.Msg)
	}
	if e.TraceID != "trace-step-1" {
		t.Fatalf("TraceID = %q, want trace-step-1", e.TraceID)
	}
	if e.Fields["agent_name"] != "OrchestratorAgent" {
		t.Fatalf("agent_name = %v", e.Fields["agent_name"])
	}
	if e.Fields["step"] != "tool_call" {
		t.Fatalf("step = %v", e.Fields["step"])
	}
	if e.Fields["tool"] != "recall.content" {
		t.Fatalf("tool = %v", e.Fields["tool"])
	}
	if e.Fields["status"] != "ok" {
		t.Fatalf("status = %v", e.Fields["status"])
	}
}

// ----------------------------------------------------------------------------
// LogPathDecision 测试
// ----------------------------------------------------------------------------

func TestStructuredLogger_LogPathDecision(t *testing.T) {
	logger, readFn, closeFn := captureLoggerOutput(t, LogLevelInfo)
	defer closeFn()
	ctx := WithTraceID(context.Background(), "trace-path-1")
	logger.LogPathDecision(ctx, "fast", 0.3)
	closeFn()
	entries := parseLogLines(t, readFn())
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Msg != "path_decision" {
		t.Fatalf("Msg = %q, want path_decision", e.Msg)
	}
	if e.TraceID != "trace-path-1" {
		t.Fatalf("TraceID = %q, want trace-path-1", e.TraceID)
	}
	if e.Fields["path"] != "fast" {
		t.Fatalf("path = %v", e.Fields["path"])
	}
	if e.Fields["complexity"] != 0.3 {
		t.Fatalf("complexity = %v, want 0.3", e.Fields["complexity"])
	}
}

// ----------------------------------------------------------------------------
// LogGraphQuery 测试
// ----------------------------------------------------------------------------

func TestStructuredLogger_LogGraphQuery(t *testing.T) {
	logger, readFn, closeFn := captureLoggerOutput(t, LogLevelInfo)
	defer closeFn()
	ctx := WithTraceID(context.Background(), "trace-graph-1")
	logger.LogGraphQuery(ctx, "MATCH (n:Tag) RETURN n LIMIT 10", int64(42), 5)
	closeFn()
	entries := parseLogLines(t, readFn())
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Msg != "graph_query" {
		t.Fatalf("Msg = %q, want graph_query", e.Msg)
	}
	if e.TraceID != "trace-graph-1" {
		t.Fatalf("TraceID = %q, want trace-graph-1", e.TraceID)
	}
	if e.Fields["cypher"] != "MATCH (n:Tag) RETURN n LIMIT 10" {
		t.Fatalf("cypher = %v", e.Fields["cypher"])
	}
	// JSON round-trip 将 int64 转为 float64，需按 float64 比较
	if got, ok := e.Fields["latency_ms"].(float64); !ok || got != 42 {
		t.Fatalf("latency_ms = %v, want 42", e.Fields["latency_ms"])
	}
	if got, ok := e.Fields["node_count"].(float64); !ok || got != 5 {
		t.Fatalf("node_count = %v, want 5", e.Fields["node_count"])
	}
}

// ----------------------------------------------------------------------------
// 无 trace_id 时不输出 trace_id 字段
// ----------------------------------------------------------------------------

func TestStructuredLogger_NoTraceID_OmitsField(t *testing.T) {
	logger, readFn, closeFn := captureLoggerOutput(t, LogLevelInfo)
	defer closeFn()
	logger.Info("no trace msg")
	closeFn()
	entries := parseLogLines(t, readFn())
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].TraceID != "" {
		t.Fatalf("TraceID = %q, want empty", entries[0].TraceID)
	}
}

// ----------------------------------------------------------------------------
// 各级别输出测试
// ----------------------------------------------------------------------------

func TestStructuredLogger_AllLevels(t *testing.T) {
	logger, readFn, closeFn := captureLoggerOutput(t, LogLevelDebug)
	defer closeFn()
	logger.Debug("d")
	logger.Info("i")
	logger.Warn("w")
	logger.Error("e")
	closeFn()
	entries := parseLogLines(t, readFn())
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4", len(entries))
	}
	want := []string{"DEBUG", "INFO", "WARN", "ERROR"}
	for i, w := range want {
		if entries[i].Level != w {
			t.Fatalf("entries[%d].Level = %q, want %q", i, entries[i].Level, w)
		}
	}
}

// ----------------------------------------------------------------------------
// LogAgentStep 从 context 提取 trace_id（无预置 trace_id）
// ----------------------------------------------------------------------------

func TestStructuredLogger_LogAgentStep_ExtractsTraceFromCtx(t *testing.T) {
	logger, readFn, closeFn := captureLoggerOutput(t, LogLevelInfo)
	defer closeFn()
	// logger 本身无 trace_id，应从 context 提取
	ctx := WithTraceID(context.Background(), "trace-extracted")
	logger.LogAgentStep(ctx, "IntentAgent", "llm_call", nil)
	closeFn()
	entries := parseLogLines(t, readFn())
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].TraceID != "trace-extracted" {
		t.Fatalf("TraceID = %q, want trace-extracted", entries[0].TraceID)
	}
}

func TestStructuredLogger_LogAgentStep_NoCtxTrace_EmptyTraceID(t *testing.T) {
	logger, readFn, closeFn := captureLoggerOutput(t, LogLevelInfo)
	defer closeFn()
	logger.LogAgentStep(context.Background(), "IntentAgent", "llm_call", nil)
	closeFn()
	entries := parseLogLines(t, readFn())
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].TraceID != "" {
		t.Fatalf("TraceID = %q, want empty", entries[0].TraceID)
	}
}
