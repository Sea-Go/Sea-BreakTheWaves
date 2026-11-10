// knowledge-serving:知识模块三路检索 serving + MCP Streamable HTTP 端点。
// 配置全部来自环境变量(与 btw content worker 同风格):
//   RTW_BASE_URL(默认 http://127.0.0.1:19289)
//   RTW_WORKER_TOKEN(必填,/etc/sea-rtw/env 的 KNOWLEDGE_WORKER_TOKEN)
//   DC_BASE_URL、DC_TOKEN(必填,BTW_DC_URL/BTW_DC_TOKEN 同值)
//   ARTIFACT_DIR(必填,/opt/sea-rtw/data/knowledge-objects)
//   INDEX_CONFIG_FILE(必填,/etc/sea-btw/search-index-config.json)
//   LISTEN(默认 127.0.0.1:19311)、MCP_TOKEN(必填)
package main

import (
	"fmt"
	"os"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func required(key string) (string, error) {
	if v := os.Getenv(key); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("缺少环境变量 %s", key)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "knowledge-serving:", err)
		os.Exit(1)
	}
}

func run() error {
	rtwToken, err := required("RTW_WORKER_TOKEN")
	if err != nil {
		return err
	}
	dcBase, err := required("DC_BASE_URL")
	if err != nil {
		return err
	}
	dcToken, err := required("DC_TOKEN")
	if err != nil {
		return err
	}
	artifactDir, err := required("ARTIFACT_DIR")
	if err != nil {
		return err
	}
	indexConfig, err := required("INDEX_CONFIG_FILE")
	if err != nil {
		return err
	}
	mcpToken, err := required("MCP_TOKEN")
	if err != nil {
		return err
	}
	listen := env("LISTEN", "127.0.0.1:19311")
	rtwBase := env("RTW_BASE_URL", "http://127.0.0.1:19289")

	searcher, err := NewSearcher(rtwBase, rtwToken, dcBase, dcToken, artifactDir, indexConfig, 8)
	if err != nil {
		return err
	}
	fmt.Printf("knowledge-serving 监听 %s(RTW %s,DC %s,artifacts %s)\n", listen, rtwBase, dcBase, artifactDir)
	return serve(listen, handler(searcher, mcpToken))
}
