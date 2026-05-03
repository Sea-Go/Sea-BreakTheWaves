# test：人机对话式测试

目的：你可以多轮提问，观察模型如何调用 tools（skills）以及决策与置信度。

## 运行

```bash
go run ./ChatTest/chat_cli.go
```

## 你将看到什么

- 工具调用顺序
- 每次工具调用的入参/出参（JSON）
- 最终回答（JSON）
- 你也可以同时打开 Jaeger，查看 trace_id 对应的链路
