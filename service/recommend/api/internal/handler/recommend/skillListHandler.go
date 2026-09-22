package recommend

import (
	"net/http"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/logic/recommend"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/svc"
	"github.com/zeromicro/go-zero/rest/httpx"
)

func SkillListHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := recommend.NewSkillListLogic(r.Context(), svcCtx)
		resp, err := l.SkillList()
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		httpx.OkJsonCtx(r.Context(), w, map[string]any{"skills": resp})
	}
}
