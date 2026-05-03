package router

import (
	"net/http"
	errmsg "sea/middleware"
	types "sea/type"

	"github.com/gin-gonic/gin"
)

// Resp 统一 API 响应结构。
// code/msg 使用 error/errmsg.go 中的定义，保证可观测与业务结果的统一表达。
// - code: 固定错误码/状态码
// - msg: 与 code 对应的可读文本
// - detail: 具体错误细节（可选）
// - trace_id: 链路追踪 id（可选）
// - data: 正常返回数据（可选）
type Resp = types.Resp

func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Resp{
		Code: errmsg.StatusOK,
		Msg:  errmsg.GetErrMsg(errmsg.StatusOK),
		Data: data,
	})
}

func Fail(c *gin.Context, httpStatus int, code int, detail string, traceID string) {
	c.JSON(httpStatus, Resp{
		Code:    code,
		Msg:     errmsg.GetErrMsg(code),
		Detail:  detail,
		TraceID: traceID,
	})
}
