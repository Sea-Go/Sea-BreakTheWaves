package main

import (
	"encoding/json"
	"net"
	"net/url"
	"os"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/sparse"
)

type liveBGERuntime struct {
	Endpoint       string `json:"endpoint"`
	AccessToken    string `json:"access_token"`
	Configurations map[string]struct {
		Callpoint       string `json:"callpoint"`
		ConfigurationID string `json:"configuration_id"`
		Profile         struct {
			Model          string                  `json:"model"`
			OutputContract string                  `json:"output_contract"`
			Space          string                  `json:"representation_space"`
			Contract       representation.Contract `json:"representation_contract"`
		} `json:"profile"`
	} `json:"configurations"`
}

func readLiveBGERuntime(t *testing.T, path string) liveBGERuntime {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var runtime liveBGERuntime
	if err := json.Unmarshal(raw, &runtime); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(runtime.Endpoint)
	if err != nil || u.Scheme != "http" || net.ParseIP(u.Hostname()) == nil ||
		!net.ParseIP(u.Hostname()).IsLoopback() || runtime.AccessToken == "" || len(runtime.Configurations) != 3 {
		t.Fatal("live BGE runtime requires a disposable loopback DC gateway and three callpoints")
	}
	for _, lane := range []string{"dense", "sparse", "token_matrix"} {
		p, ok := runtime.Configurations[lane]
		if !ok || p.Callpoint == "" || p.ConfigurationID == "" || p.Profile.Model == "" ||
			p.Profile.OutputContract == "" || p.Profile.Space == "" || p.Profile.Contract.Validate() != nil {
			t.Fatalf("live BGE %s configuration is incomplete", lane)
		}
	}
	return runtime
}

func (r liveBGERuntime) indexSettings() indexSettings {
	d, s, m := r.Configurations["dense"], r.Configurations["sparse"], r.Configurations["token_matrix"]
	return indexSettings{
		Dense: dense.Config{Document: dense.Encoding{Callpoint: d.Callpoint, ConfigurationID: d.ConfigurationID, PhysicalModel: d.Profile.Model},
			Query:    dense.Encoding{Callpoint: d.Callpoint, ConfigurationID: d.ConfigurationID, PhysicalModel: d.Profile.Model},
			Contract: d.Profile.Contract, Space: d.Profile.Space, BatchSize: 2, ProbeTopK: 2},
		Sparse: sparse.Config{Document: sparse.Encoding{Callpoint: s.Callpoint, ConfigurationID: s.ConfigurationID, PhysicalModel: s.Profile.Model},
			Query:    sparse.Encoding{Callpoint: s.Callpoint, ConfigurationID: s.ConfigurationID, PhysicalModel: s.Profile.Model},
			Contract: s.Profile.Contract, Space: s.Profile.Space, BatchSize: 2, ProbeTopK: 2},
		MultiVector: multivector.Config{Document: multivector.Encoding{Callpoint: m.Callpoint, ConfigurationID: m.ConfigurationID, PhysicalModel: m.Profile.Model},
			Query:    multivector.Encoding{Callpoint: m.Callpoint, ConfigurationID: m.ConfigurationID, PhysicalModel: m.Profile.Model},
			Contract: m.Profile.Contract, Space: m.Profile.Space, BatchSize: 2, TokenTopK: 256, ProbeTopK: 2},
	}
}
