package main

import (
	"flag"
	"fmt"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/config"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/server"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/svc"
	asyncpb "github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/pb"

	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/core/service"
	"github.com/zeromicro/go-zero/zrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

var configFile = flag.String("f", "etc/sea.async.v1.yaml", "the config file")

func main() {
	flag.Parse()

	var c config.Config
	conf.MustLoad(*configFile, &c)
	ctx := svc.NewServiceContext(c)

	s := zrpc.MustNewServer(c.RpcServerConf, func(grpcServer *grpc.Server) {
		asyncpb.RegisterAsyncServiceServer(grpcServer, server.NewAsyncServiceServer(ctx))

		if c.Mode == service.DevMode || c.Mode == service.TestMode {
			reflection.Register(grpcServer)
		}
	})
	defer s.Stop()

	fmt.Printf("Starting rpc server at %s...\n", c.ListenOn)
	s.Start()
}
