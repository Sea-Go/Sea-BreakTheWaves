# 搜索12组合评测矩阵合同

`EvaluateSearchMatrix`固定`fast/detailed × low/medium/high × summary/tools`十二个产品路径，并要求全部路径共用同一`BenchmarkRevision`、`CorpusSnapshot`、`QrelSet`、`SplitPlan`、K和逐案完整`CaseIdentity`。每格可有独立`RunRevision`、返回列表与终态；缺格、重复格、越界枚举、少case或改主体/会话/查询族/切分及时点均拒绝整个矩阵。每格调用已有`EvaluateSearch`独立计算S09/S11/S12，不将十二种不同工作负载平均成一个“胜者”指标；矩阵总`Recommendation`恒`incomplete`。

隔离`go test -mod=readonly -race -count=1 ./internal/evaluation`与`go vet -mod=readonly ./internal/evaluation`退出0。合成计算夹具覆盖十二格顺序/输入hash稳定、缺格/重复/换case拒绝，以及qrel `Complete=false`时三项相关性指标全部`not_evaluable`；未判断TopK仍计缺失分母，不变成零分。原手算Recall/MRR/nDCG回归同包通过。

这只是**评测输入和计算合同**，不是十二种真实搜索请求运行，也没有把新H10.b合成qrel声称为完整人工标注：生产者/reader当前`data_kind=synthetic`、`model_quality=null`，不能据此填写任何正式12组合提升。后续须由搜索产品和评测owner交接同内容版本的实际12路结果、已判断范围/完整性收据、RTW引用状态与真实Collector成本，再做L3/L4验收。
