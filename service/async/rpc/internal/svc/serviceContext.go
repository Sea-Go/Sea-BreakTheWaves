package svc

import (
	commonconfig "github.com/Sea-Go/Sea-BreakTheWaves/service/common/config"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/config"
)

type ServiceContext struct{ Config config.Config }

func NewServiceContext(c config.Config) *ServiceContext {
	if err := commonconfig.InitPath("etc/config.yaml"); err != nil {
		panic(err)
	}
	return &ServiceContext{Config: c}
}
