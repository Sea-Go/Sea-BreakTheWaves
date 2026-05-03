package infra

import (
	"sea/config"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// NewAIClient 创建一个通用的 OpenAI Compatible 客户端（用于 ChatCompletion / Tool Calling）。
// 按你的配置，这里走阿里 DashScope 的兼容模式。
func NewAIClient() *openai.Client {
	client := openai.NewClient(
		option.WithAPIKey(config.Cfg.Ali.APIKey),
		option.WithBaseURL(config.Cfg.Ali.BaseURL),
	)
	return &client

}
