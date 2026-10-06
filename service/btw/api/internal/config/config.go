package config

import (
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/settings"
	"github.com/zeromicro/go-zero/rest"
)

type Config struct {
	rest.RestConf
	settings.Config
}
