package main

import (
	"context"

	"agent_v3/internal/app/bootstrap"

	"trpc.group/trpc-go/trpc-agent-go/log"
)

func main() {
	if err := bootstrap.RunServer(context.Background()); err != nil {
		log.Fatalf("server exited: %v", err)
	}
}
