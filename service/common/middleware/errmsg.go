package middleware

// 说明：这些状态码/错误码用于“可观测链路”与“业务结果”的统一表达。
// - status: ok / error / degraded / fallback
// - reason/violations: 用固定 code，便于聚合与报警

const (
	// ----- Status codes -----
	StatusOK       = 200 // success
	StatusDegraded = 520 // degraded: quality/path degraded (empty retrieval, validation failed, etc.)
	StatusFallback = 521 // fallback: fallback strategy triggered (simplified path/default result, etc.)
	StatusError    = 500 // system/business error

	// ----- Error categories -----
	ErrNone        = 0   // no error
	ErrInvalidArgs = 400 // invalid parameters/input
	ErrRateLimited = 429 // Too Many Requests
	ErrUpstream5xx = 502 // upstream 5xx
	ErrTimeout     = 504 // timeout
	ErrUnknown     = 599 // unknown error fallback

	// ----- Quality/validation -----
	QualityPass                    = 1000 // pass
	QualityClaimedEvidenceButEmpty = 1001 // claimed to have evidence but retrieval is empty
	QualityMissingCitations        = 1002 // missing citations
	QualityInvalidCitation         = 1003 // invalid citation
	QualityInvalidStructure        = 1004 // invalid output structure
	QualityCitationMismatch        = 1005 // has citations but statement inconsistent with citations
	QualityTimeSkewDetected        = 1010 // time_skew_detected=true

	// ----- Tool calls -----
	ToolOK      = 2000 // tool call succeeded
	ToolFailed  = 2001 // tool call failed
	ToolTimeout = 2002 // tool call timeout

	// ----- Guardrails -----
	GuardrailAllow               = 3000 // allowed (may be redacted)
	GuardrailBlock               = 3001 // blocked
	GuardrailAllowAfterRedaction = 3002 // allowed after redaction

	// ----- Fetching -----
	FetchTriggerL2 = 4000 // trigger L2 large-field fetching
)

// Backward-compatible aliases (Deprecated).
// NOTE: Keep these for a release or two to avoid breaking downstream code.
const (
	// Deprecated: use StatusOK.
	成功 = StatusOK
	// Deprecated: use StatusDegraded.
	降级 = StatusDegraded
	// Deprecated: use StatusFallback.
	兜底 = StatusFallback
	// Deprecated: use StatusError.
	系统错误 = StatusError

	// Deprecated: use ErrNone.
	无错误 = ErrNone
	// Deprecated: use ErrInvalidArgs.
	参数不合法 = ErrInvalidArgs
	// Deprecated: use ErrRateLimited.
	限流 = ErrRateLimited
	// Deprecated: use ErrUpstream5xx.
	上游5xx = ErrUpstream5xx
	// Deprecated: use ErrTimeout.
	超时 = ErrTimeout
	// Deprecated: use ErrUnknown.
	未知错误 = ErrUnknown

	// Deprecated: use QualityPass.
	校验通过 = QualityPass
	// Deprecated: use QualityClaimedEvidenceButEmpty.
	声称有依据但为空 = QualityClaimedEvidenceButEmpty
	// Deprecated: use QualityMissingCitations.
	缺少引用 = QualityMissingCitations
	// Deprecated: use QualityInvalidCitation.
	引用无效 = QualityInvalidCitation
	// Deprecated: use QualityInvalidStructure.
	结构不合法 = QualityInvalidStructure
	// Deprecated: use QualityCitationMismatch.
	有引用但陈述不一致 = QualityCitationMismatch
	// Deprecated: use QualityTimeSkewDetected.
	检测到时间错乱 = QualityTimeSkewDetected

	// Deprecated: use ToolOK.
	工具成功 = ToolOK
	// Deprecated: use ToolFailed.
	工具失败 = ToolFailed
	// Deprecated: use ToolTimeout.
	工具超时 = ToolTimeout

	// Deprecated: use GuardrailAllow.
	护栏通过可返回 = GuardrailAllow
	// Deprecated: use GuardrailBlock.
	护栏拦截 = GuardrailBlock
	// Deprecated: use GuardrailAllowAfterRedaction.
	护栏脱敏后放行 = GuardrailAllowAfterRedaction

	// Deprecated: use FetchTriggerL2.
	触发L2抓取 = FetchTriggerL2
)

var codeMsg = map[int]string{
	// ----- Status code messages -----
	StatusOK:       "success",
	StatusDegraded: "degraded",
	StatusFallback: "fallback",
	StatusError:    "system error",

	// ----- Error category messages -----
	ErrNone:        "no error",
	ErrInvalidArgs: "invalid arguments",
	ErrRateLimited: "rate limited",
	ErrUpstream5xx: "upstream 5xx",
	ErrTimeout:     "timeout",
	ErrUnknown:     "unknown error",

	// ----- Quality/validation messages -----
	QualityPass:                    "pass",
	QualityClaimedEvidenceButEmpty: "claimed evidence but retrieval empty",
	QualityMissingCitations:        "missing citations",
	QualityInvalidCitation:         "invalid citation",
	QualityInvalidStructure:        "invalid structure",
	QualityCitationMismatch:        "citation mismatch",
	QualityTimeSkewDetected:        "time skew detected",

	// ----- Tool call messages -----
	ToolOK:      "tool success",
	ToolFailed:  "tool failed",
	ToolTimeout: "tool timeout",

	// ----- Guardrail messages -----
	GuardrailAllow:               "guardrail allow (may be redacted)",
	GuardrailBlock:               "guardrail blocked",
	GuardrailAllowAfterRedaction: "guardrail allow after redaction",

	// ----- Fetching messages -----
	FetchTriggerL2: "trigger L2 large-field fetching",
}

func GetErrMsg(code int) string {
	msg, ok := codeMsg[code]
	if !ok {
		return codeMsg[StatusError]
	}
	return msg
}
