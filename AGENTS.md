# BreakTheWaves 算法搜广推与 Agent 开发规范

本仓库的功能开发、重构、排错修复、性能优化、代码评审、架构说明及开发规范维护，均使用 [breakthewaves-workstyle](.agents/skills/breakthewaves-workstyle/SKILL.md)，不要求用户显式点名技能。按当前任务类型执行，评审／图文／规范任务不自行扩大为业务改造。

BTW 的工作对象是查询与 Ontology、完整物品、候选、特征、模型、索引、证据和反馈。实现类工作围绕算法效果与 Agent 任务完成展开；仅在 BTW 范围应用本规范。

- [项目定位与算法边界](.agents/skills/breakthewaves-workstyle/SKILL.md#项目定位与算法边界)：搜索、推荐、广告算法、Agent 和离线计算的目标与协作；外部用户侧与 BTW 物品侧的职责。
- [go-zero 与 tRPC-Agent-Go 协同规范](.agents/skills/breakthewaves-workstyle/SKILL.md#go-zero-与-trpc-agent-go-协同规范)：两边的契约、装配、算法／工具、事件、状态和关闭要求一起落实；完整实施流程见 [联合开发规范](.agents/skills/breakthewaves-workstyle/references/framework-integration.md)。
- [项目目录及职责](.agents/skills/breakthewaves-workstyle/SKILL.md#项目目录及职责)：完整真实目录树、逐目录职责、三个服务域接线、同名目录区别与新增代码落位规则。
- [修改规范](.agents/skills/breakthewaves-workstyle/SKILL.md#修改现有功能时的规范)与[新增功能规范](.agents/skills/breakthewaves-workstyle/SKILL.md#新增功能的规范)：按目标落实数据、算法、离在线消费者、实际装配与质量／代价验证。
- [能力落地细则](.agents/skills/breakthewaves-workstyle/references/writing.md)与[解耦规范](.agents/skills/breakthewaves-workstyle/references/decoupling.md)：落实召回、特征、排序、训练评测和 Agent 取证生成，避免重复计算与状态。

按技能实际加载 go-zero 和 tRPC-Agent-Go 原始规范，并执行各自的项目细则及联合接入要求。保持 go-zero 组织；通用语言规范不在此重复。算法目标与样本各按场景管理，Agent 复用实际算法能力；广告竞价、预算和计费不因“搜广推”名称自动加入当前任务。

仅实施用户当前授权范围，区分已接通功能、离线评测和生产验证。回退、图文及规范编辑使用技能对应分支；本技能不是业务运行时 Skill。
