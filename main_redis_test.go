package main

import (
	"testing"

	"github.com/codex2api/config"
)

func TestRedisConnectionTargetPrefersCompleteURL(t *testing.T) {
	cfg := config.RedisConfig{
		URL:  "rediss://acluser:secret@redis.example:6380/2",
		Addr: "redis.example:6380",
	}
	if got := redisConnectionTarget(cfg); got != cfg.URL {
		t.Fatalf("redisConnectionTarget() = %q, want complete URL %q", got, cfg.URL)
	}
}

func TestRedisConnectionTargetFallsBackToAddress(t *testing.T) {
	cfg := config.RedisConfig{Addr: "redis.internal:6379"}
	if got := redisConnectionTarget(cfg); got != cfg.Addr {
		t.Fatalf("redisConnectionTarget() = %q, want address %q", got, cfg.Addr)
	}
}
