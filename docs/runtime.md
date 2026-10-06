# 运行时

一个 go-zero 业务 API 和可独立运行的 Worker 共享领域合同。Search 按 `search_level` 选择直接检索、一次规划混合检索或按证据缺口迭代搜索。tRPC-Agent-Go 只负责需要模型的规划、工具与有据生成；确定性融合、过滤及状态提交由所属领域执行。

中间件、资源关闭、失败终态和部署切流见 [运行合同](architecture/operations.md)；当前能力与尚未验收的物理服务见 [搜索架构](architecture/search.html)和[验证报告](architecture/validation.md)。
