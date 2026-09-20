package config

import "github.com/Sea-Go/Sea-BreakTheWaves/service/common/config"

type Config struct {
	Log      config.LogConfig
	Otel     config.OtelConfig
	HTTPAddr string
	HTTPPort string
}

func Load() (Config, error) {
	if err := config.Init(); err != nil {
		return Config{}, err
	}
	return Config{Log: config.Cfg.Log, Otel: config.Cfg.Otel, HTTPAddr: config.Cfg.Services.HTTPAddr, HTTPPort: "20731"}, nil
}
