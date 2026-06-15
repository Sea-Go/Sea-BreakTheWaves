package runtime

import domaintravel "agent_v3/internal/domain/travel"

// ═══════════════════════════════════════════
// Stage 阶段常量
// ═══════════════════════════════════════════

// TravelSkillStage 定义旅行规划工作流中的每一个阶段。
// 每轮 Run() 只执行当前 stage 对应的工作（或连续推进到下一个 checkpoint）。
type TravelSkillStage string

const (
	StageRequirementIntake TravelSkillStage = "requirement_intake" // 分析需求缺口，生成追问
	StageAwaitingUserInfo  TravelSkillStage = "awaiting_user_info" // 已追问，等用户回复
	StageRequirementMerge  TravelSkillStage = "requirement_merge"  // 合并用户回复到 snapshot
	StageMacroPlanning     TravelSkillStage = "macro_planning"     // TripPlan + Phase 拆分（不含 Month/Week/Day）
	StageGraphSplitting    TravelSkillStage = "graph_splitting"    // Phase → Month → Week → Day
	StageDayExpansion      TravelSkillStage = "day_expansion"      // Day → POI → Route
	StageReview            TravelSkillStage = "review"             // 全量审查
	StageFinalOutput       TravelSkillStage = "final_output"       // 逐日 Markdown 输出
	StageDone              TravelSkillStage = "done"
	StageFailed            TravelSkillStage = "failed"
)

// ═══════════════════════════════════════════
// 字段优先级
// ═══════════════════════════════════════════

// TravelFieldLevel 标记需求字段的优先级。
type TravelFieldLevel string

const (
	FieldLevelP0 TravelFieldLevel = "P0" // 必填，缺失不能进入正式规划
	FieldLevelP1 TravelFieldLevel = "P1" // 长周期（≥30天）必须至少问一轮
	FieldLevelP2 TravelFieldLevel = "P2" // 完全可选，可直接默认
)

// ═══════════════════════════════════════════
// 默认意图
// ═══════════════════════════════════════════

// TravelDefaultIntent 表示用户是否表达"不用追问，按默认来"的意图。
type TravelDefaultIntent string

const (
	DefaultIntentNone     TravelDefaultIntent = "none"             // 没有表达默认意图
	DefaultIntentExplicit TravelDefaultIntent = "explicit_default" // 明确说"按默认/别问了/你决定"
	DefaultIntentImplicit TravelDefaultIntent = "implicit_default" // 模糊表达"都行/随便"
)

// ═══════════════════════════════════════════
// 需求快照
// ═══════════════════════════════════════════

type TravelRequirementSnapshot = domaintravel.TravelRequirementSnapshot
type DestinationAnchorSnapshot = domaintravel.DestinationAnchorSnapshot

// ═══════════════════════════════════════════
// Runtime
// ═══════════════════════════════════════════

// TravelSkillRuntime 是一个 session 在旅行规划编排中的完整运行时状态。
// 由 TravelSkillOrchestrator 管理，按 userID + sessionID 索引。
type TravelSkillRuntime struct {
	RunID     string `json:"run_id"`
	UserID    string `json:"user_id"`
	SessionID string `json:"session_id"`

	CurrentStage  TravelSkillStage `json:"current_stage"`
	PreviousStage TravelSkillStage `json:"previous_stage"`

	Requirement TravelRequirementSnapshot `json:"requirement"`

	AskedRounds  int `json:"asked_rounds"`
	MaxAskRounds int `json:"max_ask_rounds"` // 默认 2，防止无限追问

	TripPlanID string `json:"trip_plan_id"`

	LastUserMessage string `json:"last_user_message"`
	LastSkillOutput string `json:"last_skill_output"`

	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// ═══════════════════════════════════════════
// Skill 标准输出协议
// ═══════════════════════════════════════════

// SkillResult 是所有 skill 执行完成后的标准输出协议。
// Orchestrator 通过它判断是否继续、是否停止、是否追问用户。
type SkillResult struct {
	SkillName string           `json:"skill_name"`
	Stage     TravelSkillStage `json:"stage"`
	Status    string           `json:"status"` // need_user_input | ready | completed | failed | blocked

	RequirementReady bool `json:"requirement_ready"`
	InsufficientInfo bool `json:"insufficient_information"`

	MissingFields     []string `json:"missing_fields"`
	FilledFields      []string `json:"filled_fields"`
	FollowUpQuestions []string `json:"follow_up_questions"`

	Result     map[string]any `json:"result"`
	TripPlanID string         `json:"trip_plan_id"`

	NextStage    TravelSkillStage `json:"next_stage"`
	StopWorkflow bool             `json:"stop_workflow"`

	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
	Output       string `json:"output"` // 展示给用户的文本
}

// ═══════════════════════════════════════════
// Planning 决策结果
// ═══════════════════════════════════════════

// TravelPlanningDecision 是代码层对需求完整度的统一决策结果。
// 不依赖 LLM 输出，纯粹基于 snapshot + userMessage 计算。
type TravelPlanningDecision struct {
	Ready              bool                `json:"ready"`
	MissingP0          []string            `json:"missing_p0"`
	MissingP1          []string            `json:"missing_p1"`
	MissingP2          []string            `json:"missing_p2"`
	ShouldAskUser      bool                `json:"should_ask_user"`
	ShouldApplyDefault bool                `json:"should_apply_default"`
	DefaultIntent      TravelDefaultIntent `json:"default_intent"`
	Questions          []string            `json:"questions"`
}

// ═══════════════════════════════════════════
// 错误码
// ═══════════════════════════════════════════

const (
	ErrCodeParseFailed             = "parse_failed"
	ErrCodeRequirementNotReady     = "requirement_not_ready"
	ErrCodeRuntimeNotFound         = "runtime_not_found"
	ErrCodeTripPlanIDMissing       = "trip_plan_id_missing"
	ErrCodeTripPlanSessionMismatch = "trip_plan_session_mismatch"
	ErrCodeMacroPlanningFailed     = "macro_planning_failed"
)
