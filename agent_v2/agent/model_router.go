package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent_v2/config"

	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/model"
	openaimodel "trpc.group/trpc-go/trpc-agent-go/model/openai"

	openaiopt "github.com/openai/openai-go/option"
)

// ModelLevel 表示任务的智能需求等级。
type ModelLevel int

const (
	ModelLevelHigh   ModelLevel = iota // 复杂推理、多步规划、深度审查
	ModelLevelMedium                   // 清单对照、工具调用、格式检查
	ModelLevelLow                      // 摘要压缩、信息提取
)

func (l ModelLevel) String() string {
	switch l {
	case ModelLevelHigh:
		return "HIGH"
	case ModelLevelMedium:
		return "MEDIUM"
	case ModelLevelLow:
		return "LOW"
	default:
		return "UNKNOWN"
	}
}

// modelLimit 定义单个模型的 TPM 限制（来源：阿里云百炼官方文档）。
type modelLimit struct {
	Name string
	TPM  int64 // Tokens Per Minute
}

// 模型 TPM 限制表（阿里云百炼文档 2026-05）。
var modelLimits = map[string]modelLimit{
	"kimi-k2.6":           {Name: "kimi-k2.6", TPM: 1_000_000},
	"deepseek-v4-pro":     {Name: "deepseek-v4-pro", TPM: 1_200_000},
	"qwen3.6-max-preview": {Name: "qwen3.6-max-preview", TPM: 1_000_000},
	"qwen3-max":           {Name: "qwen3-max", TPM: 5_000_000},
	"glm-5.1":             {Name: "glm-5.1", TPM: 1_000_000},
	"qwen3.6-plus":        {Name: "qwen3.6-plus", TPM: 5_000_000},
}

// modelBudget 跟踪单个模型的 token 消耗。
type modelBudget struct {
	used      atomic.Int64 // 当前窗口内已消耗 token
	lastReset time.Time
}

// budgetTracker 管理所有模型的 token 预算。
type budgetTracker struct {
	mu      sync.Mutex
	budgets map[string]*modelBudget
}

var tracker = &budgetTracker{
	budgets: make(map[string]*modelBudget),
}

func init() {
	for name := range modelLimits {
		tracker.budgets[name] = &modelBudget{lastReset: time.Now()}
	}
	// 每 60 秒重置所有计数器，匹配 TPM 滑动窗口。
	go func() {
		for {
			time.Sleep(60 * time.Second)
			tracker.resetAll()
		}
	}()
}

func (t *budgetTracker) resetAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for name, b := range t.budgets {
		old := b.used.Swap(0)
		if old > 0 {
			log.Debugf("[model-budget] reset model=%s consumed=%d", name, old)
		}
		b.lastReset = now
	}
}

// SelectModel 根据 level 从对应模型池中选择剩余 TPM 配额最大的模型。
func SelectModel(level ModelLevel) string {
	pool := config.Cfg.Ali.ModelsForLevel(int(level))
	if len(pool) == 0 {
		return "deepseek-v4-pro"
	}
	if len(pool) == 1 {
		return pool[0]
	}

	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	var bestModel string
	var bestRemaining int64 = -1

	for _, name := range pool {
		limit, ok := modelLimits[name]
		if !ok {
			continue
		}
		budget, ok := tracker.budgets[name]
		if !ok {
			continue
		}
		used := budget.used.Load()
		remaining := limit.TPM - used
		if remaining > bestRemaining {
			bestRemaining = remaining
			bestModel = name
		}
	}

	if bestModel == "" {
		return pool[0]
	}

	log.Debugf("[model-select] level=%s pick=%s remaining_tpm=%d", level.String(), bestModel, bestRemaining)
	return bestModel
}

// recordUsage 记录模型消耗的 token 数。
func recordUsage(modelName string, totalTokens int) {
	if budget, ok := tracker.budgets[modelName]; ok {
		budget.used.Add(int64(totalTokens))
		limit := modelLimits[modelName]
		used := budget.used.Load()
		log.Debugf("[model-budget] model=%s used=%d/%d remaining=%d",
			modelName, used, limit.TPM, limit.TPM-used)
	}
}

// loggingModel 包装 model.Model，记录路由选择、token 用量和错误。
type loggingModel struct {
	inner     model.Model
	agentName string
	level     ModelLevel
	modelName string
}

func (m *loggingModel) Info() model.Info {
	return m.inner.Info()
}

func (m *loggingModel) GenerateContent(ctx context.Context, req *model.Request) (<-chan *model.Response, error) {
	limit := modelLimits[m.modelName]
	var used int64
	if budget := tracker.budgets[m.modelName]; budget != nil {
		used = budget.used.Load()
	}
	log.Infof("[model-router] agent=%s level=%s model=%s remaining_tpm=%d/%d",
		m.agentName, m.level.String(), m.modelName, limit.TPM-used, limit.TPM)

	ch, err := m.inner.GenerateContent(ctx, req)
	if err != nil {
		log.Errorf("[model-error] agent=%s model=%s error=%v", m.agentName, m.modelName, err)
		return nil, err
	}

	out := make(chan *model.Response)
	go func() {
		defer close(out)
		for resp := range ch {
			if resp != nil && resp.Error != nil {
				log.Errorf("[model-error] agent=%s model=%s error=%s", m.agentName, m.modelName, resp.Error.Message)
			}
			if resp != nil && resp.Done && resp.Usage != nil {
				recordUsage(m.modelName, resp.Usage.TotalTokens)
				log.Infof("[model-usage] agent=%s model=%s prompt_tokens=%d completion_tokens=%d total_tokens=%d",
					m.agentName, m.modelName,
					resp.Usage.PromptTokens,
					resp.Usage.CompletionTokens,
					resp.Usage.TotalTokens,
				)
				if emitter := traceEmitterFromContext(ctx); emitter != nil {
					emitter.EmitModelUsage(ctx, PublicModelUsage{
						AgentLabel:       publicAgentUsageLabel(m.agentName),
						Model:            m.modelName,
						ModelLevel:       m.level.String(),
						PromptTokens:     resp.Usage.PromptTokens,
						CompletionTokens: resp.Usage.CompletionTokens,
						TotalTokens:      resp.Usage.TotalTokens,
					}, stageForAgentUsage(m.agentName))
				}
			}
			out <- resp
		}
	}()
	return out, nil
}

func publicAgentUsageLabel(agentName string) string {
	switch {
	case strings.Contains(agentName, "intake"):
		return "需求识别"
	case strings.Contains(agentName, "macro") || strings.Contains(agentName, "dili360"):
		return "宏观规划"
	case strings.Contains(agentName, "amap") || strings.Contains(agentName, "phase2"):
		return "地点验证"
	case strings.Contains(agentName, "review"):
		return "审核"
	case strings.Contains(agentName, "day-output"):
		return "最终输出"
	case strings.Contains(agentName, "summary"):
		return "摘要整理"
	default:
		return "规划模型"
	}
}

func stageForAgentUsage(agentName string) string {
	switch {
	case strings.Contains(agentName, "intake"):
		return string(StageRequirementIntake)
	case strings.Contains(agentName, "macro") || strings.Contains(agentName, "dili360"):
		return string(StageMacroPlanning)
	case strings.Contains(agentName, "amap") || strings.Contains(agentName, "phase2"):
		return string(StageDayExpansion)
	case strings.Contains(agentName, "review"):
		return string(StageReview)
	case strings.Contains(agentName, "day-output"):
		return string(StageFinalOutput)
	default:
		return "planning"
	}
}

// newModelForLevel 创建一个带日志打点的模型实例。
func newModelForLevel(agentName string, level ModelLevel) model.Model {
	name := SelectModel(level)
	inner := openaimodel.New(
		name,
		openaimodel.WithBaseURL(config.Cfg.Ali.BaseURL),
		openaimodel.WithAPIKey(config.Cfg.Ali.ApiKey),
		openaimodel.WithOpenAIOptions(
			openaiopt.WithMaxRetries(5),
			openaiopt.WithHTTPClient(rateLimitHTTPClient),
		),
	)
	return &loggingModel{
		inner:     inner,
		agentName: agentName,
		level:     level,
		modelName: name,
	}
}

// newSummaryModel 创建一个带日志打点的模型实例（用于摘要和记忆提取，LOW 级别）。
func newSummaryModel(agentName string) model.Model {
	name := SelectModel(ModelLevelLow)
	inner := openaimodel.New(
		name,
		openaimodel.WithBaseURL(config.Cfg.Ali.BaseURL),
		openaimodel.WithAPIKey(config.Cfg.Ali.ApiKey),
		openaimodel.WithOpenAIOptions(
			openaiopt.WithMaxRetries(5),
			openaiopt.WithHTTPClient(rateLimitHTTPClient),
		),
	)
	return &loggingModel{
		inner:     inner,
		agentName: agentName,
		level:     ModelLevelLow,
		modelName: name,
	}
}
