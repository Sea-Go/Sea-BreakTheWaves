# 索引工件域（artifact）

本包是全文档索引工件（whole-doc index artifact）的**纯逻辑域**，覆盖四件事：

1. **清单与内容寻址 ID**（`manifest.go`）：`WholeDocIndexManifest`/`DocEntry` 的字段契约、`Validate`、`CanonicalJSON` 与 `ManifestID`。
2. **稠密/多向量量化编解码**（`codec.go`）：对称 int8 量化（scale=127/max|v|），dense 向量与 multi-token 矩阵整块共用同一布局。
3. **稀疏 impact 编码**（`sparse.go`）：`Term{TermID, Weight}` 定长 5 字节（TermID little-endian + u8 权重）编解码与 f32→u8 线性权重映射。
4. **双缓冲切换**（`switcher.go`）：`Load` 仅登记、`Activate` 原子生效，旧清单进 retained 保留 14 天。

## 边界（明确不做）

- **不做 S3 IO**：`*_ref` 字段只是外部对象存储中的引用字符串，本包不上传、下载或校验对象字节；对象读写归调用方（如 `service/common/artifacts.Store` 所在层）。
- **不做编码调用**：`encoder_id` 仅是登记信息，本包不调用任何向量化模型/DC representation 接口。
- **不做持久化**：`Switcher` 是进程内状态，重启即失忆；跨进程发布一致性由上层（outbox/对账）负责。

## 契约

- JSON 字段均为 snake_case。`WholeDocIndexManifest = {manifest_id, module_id, release_id, docs:[DocEntry]}`；`DocEntry = {doc_key, structure_ref, dense_ref, sparse_ref, multi_ref, multi_tokens, encoder_id, source_chars, budget_bytes}`。
- `Validate`：`docs≥1`；每条 doc 的 `multi_tokens∈[1,2048]`、`budget_bytes>0`、`encoder_id` 非空。
- **确定性声明**：`CanonicalJSON` 固定字段序（module_id、release_id、docs；条目内 doc_key→budget_bytes）、docs 保持原序、无缩进、最小转义。**同一 manifest 内容必产出逐字节相同的规范 JSON 与相同 `manifest_id`**；docs 顺序不同即内容不同。
- `manifest_id = hex(sha256(CanonicalJSON))[:32]`。`manifest_id` 字段本身**不参与**规范字节与哈希（自引用不可行）；因此 `ManifestID(m)` 可从内容重算，`Switcher.Load` 拒绝自报 id 与重算不一致的清单，`json.Marshal`→`Unmarshal` 往返后重算 id 必须相等（测试 `TestManifestRoundTripNoDrift` 防漂移）。

## 量化布局（codec）

```
[4 字节 little-endian float32 scale][N 字节 int8 数组]
scale = 127/max|v|（整块单一 scale；全零/非有限输入 scale=0）
```

- `QuantizeF32(v)` / `DequantizeI8(q, dim)`：dense 向量；`QuantizeMulti(mat, dim)` / `DequantizeMulti(q, rows, dim)`：multi-token 矩阵（`[]float32` 按 dim 分行）整块量化，布局同上。布局非法（长度不匹配、dim≤0、行数不符）返回 `nil`。
- 往返误差：量化步长 1/scale，最大绝对误差 ≤ max|v|/254 + float32 除法舍入，满足契约界 **≤ max|v|/127 + 1e-6**（1024 维随机向量测试断言，含极端量级 1e-30…1e30）。全零向量 scale=0，反量化为全零，**不产生 NaN/Inf**；非有限输入按 0 量化，输出永远有限。
- `RoundTripMaxErr(v)` 直接返回一次往返的最大绝对误差。

## 稀疏 impact（sparse）

- `ImpactWeight(w, maxW) = round(255·w/maxW)`，w 夹紧到 `[0,maxW]`；`maxW≤0` 或非有限输入返回 0（不产生 NaN）。
- `EncodeImpact` 保持原序定长编码；`DecodeImpact` 对长度非 5 的倍数的载荷报错。

## 双缓冲切换（Switcher）

- `NewSwitcher(clock)`：时钟注入（nil 回退 `time.Now`），测试可注入确定性时钟。
- `Load(m)`：仅登记（深拷贝），要求 `Validate` 通过且 `m.ManifestID == ManifestID(m)`；不生效。
- `Activate(id)`：id 必须已 `Load`，否则 `ErrManifestNotLoaded`；原子切 current，旧 current 进 retained，过期时刻 = `clock()+14*24h`；重复激活当前清单为 no-op；回切 retained 中的旧清单会把它移出 retained。
- `Current()` 深拷贝返回；`Retained()` 只返回未过期条目（demote 序）；`Expire(now)` 清理 `expiresAt≤now` 的条目并返回清理数。
- 并发安全（`-race` 下并发 `Current`/`Retained`/`Activate`/`Expire` 有测试）。

## 验收

```
GOCACHE=/tmp/gocache-s3 go test -race -count=1 ./service/async/rpc/internal/artifact/...
GOCACHE=/tmp/gocache-s3 go vet ./service/async/rpc/internal/artifact/...
```
