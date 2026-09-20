package types

// Resp 统一 API 响应结构。
// router 包里会通过 type alias 继续暴露为 router.Resp，保持 API 不破坏。
type Resp struct {
	Code    int    `json:"code"`
	Msg     string `json:"msg"`
	Detail  string `json:"detail,omitempty"`
	TraceID string `json:"trace_id,omitempty"`
	Data    any    `json:"data,omitempty"`
}
