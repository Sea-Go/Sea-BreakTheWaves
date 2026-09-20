package response

import (
	"net/http"

	"sea/service/common/middleware"
	"sea/service/common/types"

	"github.com/gin-gonic/gin"
)

type Resp = types.Resp

func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Resp{Code: middleware.StatusOK, Msg: middleware.GetErrMsg(middleware.StatusOK), Data: data})
}

func Fail(c *gin.Context, httpStatus, code int, detail, traceID string) {
	c.JSON(httpStatus, Resp{Code: code, Msg: middleware.GetErrMsg(code), Detail: detail, TraceID: traceID})
}
