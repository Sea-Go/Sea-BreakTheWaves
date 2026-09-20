package config

import "github.com/Sea-Go/Sea-BreakTheWaves/service/common/config"

type Config struct {
	Log      config.LogConfig
	Otel     config.OtelConfig
	HTTPAddr string
	HTTPPort string
	SkillDir string
	AsyncURL string
}

func Load() (Config, error) {
	if err := config.Init(); err != nil {
		return Config{}, err
	}
	return Config{
		Log: config.Cfg.Log, Otel: config.Cfg.Otel,
		HTTPAddr: config.Cfg.Services.HTTPAddr, HTTPPort: config.Cfg.Services.HTTPPort,
		SkillDir: "service/recommend/rpc/internal/trpcagent/skill",
		AsyncURL: "http://127.0.0.1:20741",
	}, nil
}
