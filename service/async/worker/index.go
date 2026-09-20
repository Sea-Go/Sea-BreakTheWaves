package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/sparse"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/agent/graphagent"
)

// The local exact lanes use the same typed DataCenter representation API and
// immutable build/probe contracts as their Milvus counterparts. The frozen RTW
// release is still authoritative: IndexCoordinator rejects any profile drift.
type indexSettings struct {
	Dense       dense.Config       `json:"dense"`
	Sparse      sparse.Config      `json:"sparse"`
	MultiVector multivector.Config `json:"multivector"`
}

func readIndexSettings(path string) (indexSettings, error) {
	file, err := os.Open(path)
	if err != nil {
		return indexSettings{}, fmt.Errorf("open index settings: %w", err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(raw) > 64*1024 {
		return indexSettings{}, errors.New("index settings must be a readable JSON document of at most 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var settings indexSettings
	if err := decoder.Decode(&settings); err != nil {
		return indexSettings{}, fmt.Errorf("decode index settings: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return indexSettings{}, errors.New("index settings contain trailing JSON")
	}
	return settings, nil
}

func newIndexGraph(settings indexSettings, dc *datacenter.Client, rtw *ridethewind.Client, objects artifacts.Store,
	store *content.Store, observed *telemetry.Bundle) (*graphagent.GraphAgent, error) {
	denseLane, err := dense.New(objects, dc, settings.Dense)
	if err != nil {
		return nil, fmt.Errorf("construct dense lane: %w", err)
	}
	sparseLane, err := sparse.New(objects, dc, settings.Sparse)
	if err != nil {
		return nil, fmt.Errorf("construct sparse lane: %w", err)
	}
	multiLane, err := multivector.New(objects, dc, settings.MultiVector)
	if err != nil {
		return nil, fmt.Errorf("construct multivector lane: %w", err)
	}
	indexer, err := content.NewIndexCoordinator(rtw, objects, store, denseLane, sparseLane, multiLane, observed)
	if err != nil {
		return nil, fmt.Errorf("construct fixed index coordinator: %w", err)
	}
	return content.NewIndexGraphAgent(indexer)
}
