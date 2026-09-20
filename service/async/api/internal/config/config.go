package config

type Config struct {
	HTTPAddr string
	HTTPPort string
}

func Load() Config { return Config{HTTPAddr: "0.0.0.0", HTTPPort: "20741"} }
