# evidence（B5 证据域 · 纯域层）

doc 级检索命中（源文本字符区间）→ RTW structure 契约下的 `Locator` 映射与
`EvidencePack` 组装。本包是搜索服务证据链的第一块纯域层砖：检索层给出冻结
修订的命中区间，本包把它变成可跨服务传输、可被 RTW `structure.Anchor`
反向验证的证据地址。

## 职责

- `MapHitToLocator(tree, source, charStart, charEnd)`（locator.go）：单条命中
  区间 → `Locator`。区间必须完整落在单一段落节点（level 7）内——跨段、落在
  标题行或节点间空白带上均报 `ErrHitCrossParagraph`（"命中跨段，请按段细分"，
  可 `errors.Is` 判别），调用方应按段细分后重试。`section_path` 按文档序扫描
  该段落之前的全部标题、维护层级栈（level ≤ 栈顶则弹栈再压入）得到从根出发
  层级严格递增的标题链；`quote` 取区间文本 `TrimSpace` 后的前 200 rune。
- `BuildPack(queryID, hits)`（pack.go）：`DocHit` 列表 → `EvidencePack`。
  逐 hit 调 `MapHitToLocator`，**任一 hit 失败整包失败**（全有或全无，绝不
  产出部分证据的包）；候选按 `rrf_score` 降序、平局按 `doc_key` 字典序
  （稳定排序，同输入同输出）；出口前再跑一次 `Validate` 把关。
- `EvidencePack.Validate()`（types.go）：出口契约校验——`query_id` 非空、
  候选 ≤ 50、每候选 evidence 1..8 条 `Locator`、每条 quote 非空且 ≤ 200
  rune、`doc_key` 非空。跨服务收到 JSON 反序列化后也可用它做入口校验。

## 边界（不能做什么）

- **不做检索打分**：`rrf_score` 与 `lanes`（dense/sparse/multi/rerank，
  rerank=0 表"未经过 rerank"）由召回与融合层产出，本包只透传与排序。
- **不做持久化 / 网络 / 存储**：纯函数包，无 IO，可独立测试。
- **不做 Markdown 解析**：结构树（`TreeJSON`）由 RTW `structure.Derive`
  在冻结修订时产出，本包只消费镜像契约，不重复实现派生。

## 与 RTW structure 的镜像关系声明

本包**不 import Sea-RideTheWaves 仓代码**（跨仓 `internal` 不可导入，亦为
本仓架构红线），而是持有 RTW `service/knowledge/internal/structure` 契约的
镜像类型：`TreeJSON`/`NodeJSON` ↔ `Tree`/`Node`，`Locator`（增补
`revision_id` 字段以支持跨文档 EvidencePack）。字段名、语义与 JSON tag
（snake_case）**字段级一致**：

| BTW 镜像（本包） | RTW structure | 说明 |
|---|---|---|
| `TreeJSON{revision_id, nodes[]}` | `Tree{RevisionID, Nodes}` | 冻结修订结构树 |
| `NodeJSON{node_id, level, title, para_index, char_start, char_end}` | `Node` | 标题 level 1..6 / 段落 7；标题 `para_index=-1`；字节区间 `[start,end)` |
| `Locator{revision_id, section_path, para_index, quote}` | `Locator{SectionPath, ParaIndex, Quote}` | BTW 侧增补 `revision_id`；`quote ≤ 200 rune`（`MaxQuoteRunes` 与 RTW 一致） |

**改动需双边同步**：任何一侧增删字段、改 JSON tag 或改常量（如 200 rune
上限、level 7 段落约定），必须同步修改另一侧并双边发版，否则跨服务证据
寻址会静默错位。测试 `TestJSONTags_Mirror` 钉死了线格式。

## 验收

```bash
go vet ./service/search/rpc/internal/evidence/...
GOCACHE=/tmp/gocache-s5 go test -race ./service/search/rpc/internal/evidence/...
```

接线（检索层产出 DocHit、EvidencePack 出口到 Agent 引用链）在后续装配
里程碑完成，本包不依赖它们。
