// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.2

package main

import (
	"context"
	"flag"
	"fmt"

	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/datacenter"
	"github.com/zeromicro/go-zero/core/proc"
)

var configFile = flag.String("f", "etc/btw-api.yaml", "the config file")

func main() {
	flag.Parse()
	app, err := btwruntime.NewWithDataCenterIdentity(context.Background(), *configFile, datacenter.DeploymentIdentity())
	if err != nil {
		panic(err)
	}
	defer app.Close()
	proc.AddShutdownListener(func() { _ = app.Close() })
	fmt.Printf("Starting server at %s:%d...\n", app.Host, app.Port)
	app.Server.Start()
}
