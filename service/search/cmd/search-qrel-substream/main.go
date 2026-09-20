// Command search-qrel-substream exports only explicitly synthetic, default-off
// RTW/DC/PG fixture revisions. It has no observed-human export mode.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/internal/warehouse/searchsource"
	"github.com/jackc/pgx/v5/pgxpool"
)

func writeNew(path string, body []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func run(ctx context.Context, connectionFile, groupingFile, outputDir string, through int64) (
	searchsource.SubstreamManifest, error) {
	var source struct {
		DSN          string `json:"dsn"`
		Consumer     string `json:"consumer"`
		Producer     string `json:"producer"`
		AcceptedTest string `json:"accepted_test"`
	}
	connectionRaw, err := os.ReadFile(connectionFile)
	if err != nil {
		return searchsource.SubstreamManifest{}, err
	}
	if err := json.Unmarshal(connectionRaw, &source); err != nil || source.DSN == "" ||
		source.Consumer != searchsource.DefaultConsumer || source.Producer != searchsource.Producer ||
		source.AcceptedTest == "" {
		return searchsource.SubstreamManifest{}, searchsource.ErrContract
	}
	groupingRaw, err := os.ReadFile(groupingFile)
	if err != nil {
		return searchsource.SubstreamManifest{}, err
	}
	policy, policySHA, err := searchsource.ParseGroupingPolicy(groupingRaw)
	if err != nil || policy.FixtureProvenance != source.AcceptedTest {
		return searchsource.SubstreamManifest{}, searchsource.ErrContract
	}
	pool, err := pgxpool.New(ctx, source.DSN)
	if err != nil {
		return searchsource.SubstreamManifest{}, err
	}
	defer pool.Close()
	bundle, err := searchsource.ExportSyntheticSubstream(ctx, pool, through, policy, policySHA)
	if err != nil {
		return searchsource.SubstreamManifest{}, err
	}
	if err := searchsource.ValidateCoverage(bundle); err != nil {
		return searchsource.SubstreamManifest{}, err
	}
	if err := os.Mkdir(outputDir, 0700); err != nil {
		return searchsource.SubstreamManifest{}, err
	}
	manifestRaw, err := json.MarshalIndent(bundle.Manifest, "", "  ")
	if err != nil {
		return searchsource.SubstreamManifest{}, err
	}
	for name, body := range map[string][]byte{
		"manifest.json":  append(manifestRaw, '\n'),
		"coverage.jsonl": bundle.CoverageJSONL,
		bundle.Manifest.LandingBatchID + ".jsonl": bundle.LandingJSONL,
		"grouping.json": groupingRaw,
	} {
		if err := writeNew(filepath.Join(outputDir, name), body); err != nil {
			return searchsource.SubstreamManifest{}, err
		}
	}
	return bundle.Manifest, nil
}

func main() {
	var connectionFile, groupingFile, outputDir string
	var through int64
	var syntheticFixture bool
	flag.StringVar(&connectionFile, "connection-file", "", "private local test PG connection receipt")
	flag.StringVar(&groupingFile, "grouping-policy", "", "versioned synthetic family/near-duplicate mapping")
	flag.StringVar(&outputDir, "output", "", "new immutable output directory")
	flag.Int64Var(&through, "through-offset", 0, "complete original DC producer prefix end")
	flag.BoolVar(&syntheticFixture, "synthetic-fixture", false, "explicitly enable synthetic fixture export")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if !syntheticFixture || connectionFile == "" || groupingFile == "" || outputDir == "" || through < 1 {
		logger.Error("synthetic fixture mode and all frozen inputs are required")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	manifest, err := run(ctx, connectionFile, groupingFile, outputDir, through)
	if err != nil {
		logger.Error("synthetic qrel substream export failed", "error", err)
		os.Exit(1)
	}
	logger.Info("synthetic qrel substream exported", "output", outputDir,
		"original_dc_offset", manifest.ThroughOffset, "qrel_ordinal", manifest.QrelCount,
		"technical_skips", manifest.TechnicalSkipCount, "coverage_root", manifest.CoverageRoot,
		"activation", manifest.Activation)
}
