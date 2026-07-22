package config

import (
	"strings"
	"testing"
)

func TestLoadRejectsPushAddrEqualToAdminAddr(t *testing.T) {
	t.Setenv("STATE_STORE_DSN", "postgres://x/y")
	t.Setenv("HTTP_ADDR", ":8080")
	t.Setenv("PUSH_HTTP_ADDR", ":8080") // collision — must be rejected (ADR-011)

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when PUSH_HTTP_ADDR equals HTTP_ADDR")
	}
	if !strings.Contains(err.Error(), "PUSH_HTTP_ADDR") {
		t.Fatalf("error should mention PUSH_HTTP_ADDR, got: %v", err)
	}
}

func TestLoadAllowsDistinctPushAddr(t *testing.T) {
	t.Setenv("STATE_STORE_DSN", "postgres://x/y")
	t.Setenv("HTTP_ADDR", ":8080")
	t.Setenv("PUSH_HTTP_ADDR", ":9090")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PushHTTPAddr != ":9090" {
		t.Fatalf("PushHTTPAddr = %q, want :9090", cfg.PushHTTPAddr)
	}
}

func TestLoadEmptyPushAddrDisablesPush(t *testing.T) {
	t.Setenv("STATE_STORE_DSN", "postgres://x/y")
	// PUSH_HTTP_ADDR unset → empty → push disabled, no collision error.
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PushHTTPAddr != "" {
		t.Fatalf("PushHTTPAddr = %q, want empty", cfg.PushHTTPAddr)
	}
}
