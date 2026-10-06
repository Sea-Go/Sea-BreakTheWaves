package config

import (
	"github.com/zeromicro/go-zero/core/conf"
	"testing"
)

func TestExampleConfigurationLoadsWithoutSeaConfig(t *testing.T) {
	var c Config
	if e := conf.Load("../../etc/btw-api.yaml", &c, conf.UseEnv()); e != nil {
		t.Fatal(e)
	}
	if c.Storage.Schema != "btw" || c.Scenarios["website"].Delivery != "list" || c.Scenarios["website"].Kinds[0] != "article" {
		t.Fatal("configuration did not reach serving policy")
	}
}
