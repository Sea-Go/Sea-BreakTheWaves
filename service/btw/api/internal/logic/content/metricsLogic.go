// Code scaffolded by goctl. Safe to edit.
package content

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"net/http"
)

type MetricsLogic struct{ svc *svc.ServiceContext }

func NewMetricsLogic(_ context.Context, s *svc.ServiceContext) *MetricsLogic {
	return &MetricsLogic{svc: s}
}
func (l *MetricsLogic) Metrics() (http.Handler, error) {
	if l.svc.Observed == nil {
		return nil, item.ErrUnavailable
	}
	return l.svc.Observed.MetricsHandler(), nil
}
