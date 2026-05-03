package zlog

import (
	"errors"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

var zlog *zap.Logger

// InitLogger 初始化日志。
// logPath: 日志文件路径
// logLevel: debug/info/error
// serviceName: 服务名称（会写入每条日志的固定字段）

// 返回值：如果日志目录创建失败，会返回 error。
func InitLogger(logPath string, logLevel string, serviceName string) error {
	if logPath == "" {
		return errors.New("日志路径为空：请在 config.yaml 中配置 log.path")
	}

	// 确保日志目录存在
	if dir := filepath.Dir(logPath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	// 日志分割
	hook := lumberjack.Logger{
		Filename:   logPath, // 日志文件路径
		MaxSize:    10,      // 每个日志文件保存 10MB
		MaxBackups: 30,      // 保留 30 个备份
		MaxAge:     7,       // 保留 7 天
		Compress:   true,    // 是否压缩
	}

	write := zapcore.AddSync(&hook)

	// 设置日志级别：debug -> info -> warn -> error
	var level zapcore.Level
	switch logLevel {
	case "debug":
		level = zap.DebugLevel
	case "info":
		level = zap.InfoLevel
	case "error":
		level = zap.ErrorLevel
	default:
		level = zap.InfoLevel
	}

	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "linenum",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder, // 小写编码器
		EncodeTime:     zapcore.ISO8601TimeEncoder,    // ISO8601 UTC 时间格式
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.FullCallerEncoder, // 全路径编码器
		EncodeName:     zapcore.FullNameEncoder,
	}

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.NewMultiWriteSyncer(zapcore.AddSync(os.Stdout), write), // 控制台 + 文件
		level,
	)

	// caller：开启文件及行号
	caller := zap.AddCaller()
	development := zap.Development()
	filed := zap.Fields(zap.String("serviceName", serviceName))

	zlog = zap.New(core, caller, development, filed)
	L().Info("日志系统初始化完成", zap.String("日志级别", logLevel), zap.String("日志文件", logPath))
	return nil
}

func L() *zap.Logger {
	if zlog == nil {
		// 防止忘记 Init 导致 panic：给一个 fallback
		l, _ := zap.NewProduction()
		return l
	}
	return zlog
}

// S 返回 sugared（printf 风格）。
// ⚠️ 尽量少用 printf 风格，推荐结构化字段。
func S() *zap.SugaredLogger {
	return L().Sugar()
}

// Sync 程序退出前调用，确保日志刷盘。
func Sync() {
	_ = L().Sync()
	_ = os.Stdout.Sync()
}
