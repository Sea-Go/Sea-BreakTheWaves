package svc

import "github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/config"

type ServiceContext struct {
	Config config.Config
}

func NewServiceContext(c config.Config) *ServiceContext {
	return &ServiceContext{Config: c}
}
