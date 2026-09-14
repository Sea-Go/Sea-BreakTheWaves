# BGE-M3 表示服务

使用官方 `BGEM3FlagModel` 和固定 `BAAI/bge-m3` 权重，为 DataCenter 提供 Dense、学习型 Sparse、Multi-vector 三种真实表示。该进程只负责编码；检索、排序、质量评测及产品发布由对应工程负责。

## 固定来源

- 模型：[`BAAI/bge-m3@5617a9f61b028005a4858fdac845db406aefb181`](https://huggingface.co/BAAI/bge-m3/tree/5617a9f61b028005a4858fdac845db406aefb181)，模型卡声明 MIT。仅下载 9 个必需文件，共 `2,295,435,096` bytes，文件大小和 SHA256 见 [`model.lock.json`](model.lock.json)。没有下载 ONNX 等重复导出。
- 推理：[`FlagEmbedding 1.3.5`](https://pypi.org/project/FlagEmbedding/1.3.5/)，MIT；sdist 及执行源码 SHA256 见 [`source.lock.json`](source.lock.json)。框架自行执行模型前向、三个投影头、归一化及 token 处理，本项目不复制训练/推理源码。
- 环境：Python 3.12、PyTorch 2.6.0、Transformers 4.44.2、NumPy 1.26.4；全部传递依赖固定在独立 [`uv.lock`](uv.lock)，不修改根训练环境或全局 Python。当前验证平台为 macOS ARM64 CPU。
- 协议：DataCenter `4f5abf571482a697b4175bcc87d15ed4ab6c6ee4` 的 Sea representations v1，使用该版显式 `mean_maxsim`。旧 `317ce623` 只接受 `sum_maxsim`，不能用于本模型的多向量 profile。

工作区域：[W0] 本独立 BTW worktree；[W1] `training/serving/bge_m3/`；[R1] DC 公开表示合同、BTW SDK；[D1] 固定 FlagEmbedding/HF 源；[G1] uv/model/source lock 与 profile；[X1] 官方下载及任务回环 HTTP；[N1] Go 模块、DC/RTW 业务代码和生产；[T1] 核对源码、验收日志与模型缓存。模型权重与虚拟环境不提交 Git。

## 数值合同

完整配置见 [`profiles.json`](profiles.json)。三个 profile 共用实际模型 `BAAI/bge-m3@5617a9f61b02` 和固定 tokenizer；query/document 不添加检索提示词，保持官方 BGE-M3 输入方法。

| 类型 | 官方输出 | 声明和处理 |
| --- | --- | --- |
| Dense | `dense_vecs` | 1024 维、FP32 输出、L2 单位向量、dot；不由稀疏或 token 向量拼造 |
| Sparse | `lexical_weights` | 250002 维词表空间；学习型 token 权重，重复 token 取官方 max；按 token ID 排序，保留正权重，不另做 L2 或 BM25 |
| Multi-vector | `colbert_vecs` | 每个有效 token 一条 1024 维 L2 向量；官方已去 CLS 和 padding，保留 EOS；输出 mask 全 true；`mean_maxsim` |

对 token 矩阵，评分是每个有效 query token 在有效 document token 中取最大内积，再除以有效 query token 数。**mean 与 sum 不互换**；profile ID/space 固定绑定该选择。官方实现见 [`colbert_score`、重复 token 处理和去 padding](https://github.com/FlagOpen/FlagEmbedding/blob/fd1a2bdf69488ffebe0327999d4400d8c8058a0b/FlagEmbedding/inference/embedder/encoder_only/m3.py)；实际执行版本以本地锁定 1.3.5 sdist 的源码哈希为准。

本进程固定每批最多 2 条、每条最多 128 tokens（含特殊 token），最多 127 条有效 ColBERT 向量和 126 个非零词项。超长输入明确拒绝，要求上游先分块，不静默截断。CPU 运算线程为 2，interop 为 1，关闭 fp16 和 tokenizer 并行；同时只允许一批推理，忙时返回 429。

## 下载与启动

在本目录执行：

```bash
uv sync --locked --python 3.12
python3 fetch_model.py
```

下载器仅访问锁定官方 URL，支持同一文件断点续传，完成后核对长度和 SHA256。默认模型目录为 `~/.cache/sea-models/bge-m3/<完整 revision>`；也可用 `--directory` 指定任务缓存。启动时再次校验所有权重和 tokenizer 文件，随后只使用本地模型，关闭 HF 在线拉取。

```bash
sea_bge_model="$HOME/.cache/sea-models/bge-m3/5617a9f61b028005a4858fdac845db406aefb181"
uv run --locked sea-bge-m3 \
  --model-directory "$sea_bge_model" \
  --model-lock model.lock.json \
  --port 0
```

程序加载后输出一行 JSON `ready`，其中含实际随机端口。服务默认仅绑定 `127.0.0.1`；SIGTERM/Ctrl-C 后等待当前请求退出并关闭。安装和 import 均不会自动启动服务。首次载入和推理应预留时间；客户端断连不会发起重试，已开始的单批 CPU 推理会在有界输入上结束，不承诺内核级即时中断。

## 给 DataCenter 的交接

- Provider BaseURL 使用 `http://127.0.0.1:<port>/v1`。
- `GET /v1/models` 返回上述实际模型 ID；`POST /v1/representations` 接收 Sea typed body。
- 注册三个独立 embedding 配置，分别使用 profiles.json 中的 `output_contract`、`representation_space`、完整 `representation_contract`；Sparse 的维数是 250002，Dense/Multi-vector 为 1024。
- query/document 的 configuration ID 由 DataCenter 实际登记返回。本服务原样回显该固定 ID、role、contract ID 和 space；不合成 DataCenter 业务配置。
- 请求的 model 是 Gateway 解析后的实际模型名。响应保留 typed data、tokenizer/词表和实际 token usage；aggregation 属于固定 profile，不向既定响应追加未知字段。
- 先通过 DC 两角色 probe，再发布调用点，随后使用 BTW 的共享 typed SDK 消费。生产调用通过 DC 调度，本目录的直连测试仅验证 Provider 边界。

服务错误不转换为空向量：非法字段/空间/模型、重复 ID/JSON key、超限请求返回 422；繁忙返回 429；模型执行故障返回 500。每个 payload 在返回前检查维数、有限值、非零范数和声明的 L2。无有效稀疏权重时失败，不补虚构词项。

## 已验证结果

2026-09-14：13 个合同用例、2 个真实模型用例通过。真实用例加载官方权重，逐元素对照官方原始前向：Dense、Sparse 和 ColBERT 均一致；重复 token 的 max 与 sum 实际有差异；去 padding/CLS 后的行数、范数、平均 MaxSim、超长拒收与繁忙响应均通过。

```bash
uv run --locked pytest -q tests/test_contract.py
SEA_BGE_MODEL_DIRECTORY="$sea_bge_model" \
  uv run --locked pytest -s -v tests/test_real.py
uv run --locked python acceptance.py --model-directory "$sea_bge_model"
```

最后一条命令启动独立 Provider 子进程，分别调用真实三种 HTTP 输出，记录响应文件与摘要，并在结束前 SIGTERM + wait。已执行结果见 [`acceptance.json`](acceptance.json)：

- Dense `[2,1024]`；Sparse 非零项 `5/12`；token 矩阵 `[10,1024]`、`[13,1024]`。
- 固定自建短文本的 dense dot `0.5261532838`、sparse dot `0.0085858762`、mean MaxSim `0.4751812463`。这些只用于数值对照，不是检索效果指标。
- 独立进程三次 HTTP 请求约 `3.74s / 1.89s / 1.80s`，含载入总计 `24.64s`；观测峰值 RSS `1,801,125,888` bytes。测量只适用于本机及这组短输入。
- 模型进程已停止。权重和环境保留在任务缓存，供 DC 隔离联验复用。

尚不宣称 DC 完整控制面链路、三路检索质量、生产吞吐、GPU/Linux 部署或整个 H05 已完成；相应验收由集成任务记录。
