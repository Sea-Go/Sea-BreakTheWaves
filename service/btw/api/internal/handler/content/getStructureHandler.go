// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.2

package content

import (
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"net/http"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/logic/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/types"
	"github.com/zeromicro/go-zero/rest/httpx"
)

func GetStructureHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.StructureRequest
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, fmt.Errorf("%w: %v", item.ErrInvalid, err))
			return
		}

		l := content.NewGetStructureLogic(r.Context(), svcCtx)
		resp, err := l.GetStructure(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
		} else {
			httpx.OkJsonCtx(r.Context(), w, resp)
		}
	}
}
