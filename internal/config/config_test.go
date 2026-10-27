package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 3003 || c.Addr() != ":3003" || c.DatabasePath != "data/orders.sqlite" || !c.SeedDemo ||
		c.LogLevel != slog.LevelInfo || c.SSEHeartbeat != 20*time.Second || c.OperatorPassword != "" {
		t.Fatalf("unexpected defaults %+v", c)
	}
}

func TestOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"PORT": "3207", "HOST": "127.0.0.1", "LOG_LEVEL": "debug", "SEED_DEMO": "false",
		"OPERATOR_PASSWORD": "uma-senha-longa", "SSE_HEARTBEAT": "5s", "TRUST_PROXY": "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr() != "127.0.0.1:3207" || c.LogLevel != slog.LevelDebug || c.SeedDemo || !c.TrustProxy || c.SSEHeartbeat != 5*time.Second {
		t.Fatalf("unexpected %+v", c)
	}
}

func TestInvalidValuesAreReportedTogether(t *testing.T) {
	_, err := Load(env(map[string]string{
		"PORT": "99999", "LOG_LEVEL": "loud", "SEED_DEMO": "maybe", "SSE_HEARTBEAT": "1ms", "OPERATOR_PASSWORD": "curta",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, key := range []string{"PORT", "LOG_LEVEL", "SEED_DEMO", "SSE_HEARTBEAT", "OPERATOR_PASSWORD"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not mention %s: %v", key, err)
		}
	}
}
