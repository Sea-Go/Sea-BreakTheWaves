// Command worker runs BTW-owned ingestion, processing, indexing and samples.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/analytics"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/settings"
	"github.com/zeromicro/go-zero/core/conf"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil && !errors.Is(e, context.Canceled) {
		slog.Error("worker stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	configFile := flag.String("f", "service/btw/api/etc/btw-api.yaml", "configuration")
	command := flag.String("command", "process", "prepare|consume|process|export-feedback|aggregate-feedback|behavior-samples|status|samples|build-index|retry|gc-originals")
	id := flag.String("id", "", "job id")
	output := flag.String("output", "", "sample output file")
	sampleKind := flag.String("sample-kind", "rank", "rank|embedding sample target")
	objective := flag.String("objective", "search", "search|recommend")
	scenarioName := flag.String("scenario", "", "training scenario; defaults to search or website by objective")
	asOfArg := flag.String("as-of", "", "feedback observation cutoff RFC3339; default current time")
	observeWindow := flag.Duration("observation-window", 24*time.Hour, "negative feedback observation window")
	gcApply := flag.Bool("apply", false, "apply orphan cleanup; default is dry-run")
	gcAge := flag.Duration("grace", 24*time.Hour, "orphan commit grace")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var cfg settings.Config
	if e := conf.Load(*configFile, &cfg, conf.UseEnv()); e != nil {
		return e
	}
	compute := *command == "process" || *command == "prepare" || *command == "build-index" || *command == "samples"
	service, infer, close, e := content.OpenWorkerWithModel(ctx, cfg, compute, nil, datacenter.DeploymentIdentity())
	if e != nil {
		return e
	}
	defer close()
	switch *command {
	case "prepare":
		if e = infer.Ready(ctx, service.Retrieval()); e != nil {
			return e
		}
		return service.Prepare(ctx)
	case "consume":
		return service.Consume(ctx, cfg.Kafka.Brokers, cfg.Kafka.ContentTopic, cfg.Kafka.Group)
	case "process":
		for ctx.Err() == nil {
			worked, e := service.RunOnce(ctx)
			if e != nil {
				slog.Error("content attempt failed", "error", e)
			}
			if !worked || e != nil {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Second):
				}
			}
		}
		return ctx.Err()
	case "behavior-samples":
		asOf := time.Now().UTC()
		if *asOfArg != "" {
			asOf, e = time.Parse(time.RFC3339, *asOfArg)
			if e != nil {
				return e
			}
		}
		scenarios := []string{}
		for name, scenario := range cfg.Scenarios {
			if (*objective == "search" && scenario.Delivery == "search") || (*objective == "recommend" && (scenario.Delivery == "list" || scenario.Delivery == "plan")) {
				scenarios = append(scenarios, name)
			}
		}
		var w io.Writer = os.Stdout
		if *output != "" {
			f, e := os.Create(*output)
			if e != nil {
				return e
			}
			defer f.Close()
			w = f
		}
		sink := &analytics.ClickHouse{URL: cfg.Analytics.URL, Database: cfg.Analytics.Database, User: cfg.Analytics.User, Password: cfg.Analytics.Password}
		return sink.ExportTraining(ctx, scenarios, asOf, *observeWindow, w)
	case "export-feedback":
		for ctx.Err() == nil {
			_, e := service.ExportBehavior(ctx, cfg.Kafka.Brokers, cfg.Kafka.BehaviorTopic)
			if e != nil {
				return e
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
		return ctx.Err()
	case "aggregate-feedback":
		sink := &analytics.ClickHouse{URL: cfg.Analytics.URL, Database: cfg.Analytics.Database, User: cfg.Analytics.User, Password: cfg.Analytics.Password}
		return content.ConsumeBehavior(ctx, cfg.Kafka.Brokers, cfg.Kafka.BehaviorTopic, cfg.Kafka.Group+"-analytics", sink)
	case "status":
		v, e := service.Job(ctx, *id)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(v)
	case "gc-originals":
		return service.GCOriginals(ctx, *gcAge, *gcApply, os.Stdout)
	case "retry":
		return service.Retry(ctx, *id)
	case "samples":
		if *sampleKind != "rank" && *sampleKind != "embedding" {
			return fmt.Errorf("sample-kind must be rank or embedding")
		}
		var w io.Writer = os.Stdout
		if *output != "" {
			f, e := os.Create(*output)
			if e != nil {
				return e
			}
			defer f.Close()
			w = f
		}
		name := *scenarioName
		if name == "" {
			name = "search"
			if *objective == "recommend" {
				name = "website"
			}
		}
		scenario, ok := cfg.Scenarios[name]
		if !ok {
			return fmt.Errorf("unknown training scenario")
		}
		return service.BuildSamples(ctx, w, *sampleKind == "embedding", scenario)
	case "build-index":
		cursor, err := service.BuildIndexes(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]int64{"source_change_cursor": cursor})
	default:
		return fmt.Errorf("unknown command %q", *command)
	}
}
