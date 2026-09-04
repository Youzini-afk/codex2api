package main

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
)

func TestApplyRuntimeSystemSettingsPublishesExternalProjections(t *testing.T) {
	t.Setenv("CODEX_BILLING_TIER_POLICY", proxy.BillingTierPolicyRequested)
	previousRuntime := proxy.CurrentRuntimeSettings()
	t.Cleanup(func() {
		proxy.ApplyRuntimeSettings(previousRuntime)
		database.SetModelPricingOverrides(nil)
	})

	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "runtime-settings.db"))
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	limiter := proxy.NewRateLimiter(3)
	t.Cleanup(limiter.GetEnhancedLimiter().Stop)
	settings := &database.SystemSettings{
		GlobalRPM:                     87,
		UsageLogMode:                  database.UsageLogModeErrors,
		UsageLogBatchSize:             19,
		UsageLogFlushIntervalSeconds:  7,
		BillingTierPolicy:             proxy.BillingTierPolicyActual,
		CodexFastModelAliasEnabled:    false,
		CodexFastTierInterceptEnabled: true,
		CodexRequestCompression:       false,
		ModelPricingOverrides:         `{"gpt-settings-sync":{"source":"custom","input":1.25}}`,
	}

	runtimeSettings, err := applyRuntimeSystemSettings(context.Background(), settings, db, limiter)
	if err != nil {
		t.Fatalf("applyRuntimeSystemSettings: %v", err)
	}
	if runtimeSettings.BillingTierPolicy != proxy.BillingTierPolicyRequested {
		t.Fatalf("BillingTierPolicy = %q, want env override requested", runtimeSettings.BillingTierPolicy)
	}
	if runtimeSettings.CodexFastModelAliasEnabled || !runtimeSettings.CodexFastTierInterceptEnabled || runtimeSettings.CodexRequestCompression {
		t.Fatalf("proxy runtime settings = %+v", runtimeSettings)
	}
	if limiter.GetRPM() != 87 {
		t.Fatalf("rate limiter RPM = %d, want 87", limiter.GetRPM())
	}
	if db.GetUsageLogMode() != database.UsageLogModeErrors || db.GetUsageLogBatchSize() != 19 || db.GetUsageLogFlushIntervalSeconds() != 7 {
		t.Fatalf("usage log config = %s/%d/%d", db.GetUsageLogMode(), db.GetUsageLogBatchSize(), db.GetUsageLogFlushIntervalSeconds())
	}
	if source := database.ModelPricingSourceFor("gpt-settings-sync"); source != database.ModelPricingSourceCustom {
		t.Fatalf("pricing source = %q, want custom", source)
	}

	// Replaying the same committed snapshot must be harmless.
	if _, err := applyRuntimeSystemSettings(context.Background(), settings, db, limiter); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
}

func TestSystemSettingsOutboxHookUpdatesProcessRuntime(t *testing.T) {
	t.Setenv("CODEX_BILLING_TIER_POLICY", "")
	t.Setenv("CODEX_SCHEDULER_ENGINE", "")
	previousRuntime := proxy.CurrentRuntimeSettings()
	t.Cleanup(func() { proxy.ApplyRuntimeSettings(previousRuntime) })
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "runtime-settings-outbox.db"))
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	settings := &database.SystemSettings{
		MaxConcurrency:                     2,
		GlobalRPM:                          5,
		TestModel:                          "gpt-5.4",
		TestContent:                        auth.DefaultTestContent,
		TestConcurrency:                    4,
		BackgroundRefreshIntervalMinutes:   2,
		UsageProbeMaxAgeMinutes:            10,
		UsageProbeConcurrency:              4,
		UsageProbeResponsesFallbackEnabled: true,
		RecoveryProbeIntervalMinutes:       30,
		SchedulerMode:                      "round_robin",
		AffinityMode:                       auth.AffinityModeBounded,
		SchedulerEngine:                    "legacy",
		CodexRequestCompression:            true,
		CodexFastModelAliasEnabled:         true,
		UsageLogMode:                       database.UsageLogModeFull,
		UsageLogBatchSize:                  20,
		UsageLogFlushIntervalSeconds:       5,
		ModelPricingOverrides:              "{}",
	}
	if err := db.UpdateSystemSettings(ctx, settings); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	persisted, err := db.GetSystemSettings(ctx)
	if err != nil {
		t.Fatalf("GetSystemSettings: %v", err)
	}
	limiter := proxy.NewRateLimiter(persisted.GlobalRPM)
	store := auth.NewStore(db, nil, persisted)
	store.SetSystemSettingsApplyHook(systemSettingsApplyHook(db, limiter))
	t.Cleanup(func() {
		store.Stop()
		limiter.GetEnhancedLimiter().Stop()
		_ = db.Close()
	})
	if err := store.Init(ctx); err != nil {
		t.Fatalf("Store.Init: %v", err)
	}

	updated := *persisted
	updated.GlobalRPM = 111
	updated.CodexFastModelAliasEnabled = false
	updated.CodexFastTierInterceptEnabled = true
	updated.CodexRequestCompression = false
	if err := db.UpdateSystemSettings(ctx, &updated); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runtimeSettings := proxy.CurrentRuntimeSettings()
		if limiter.GetRPM() == 111 && !runtimeSettings.CodexFastModelAliasEnabled &&
			runtimeSettings.CodexFastTierInterceptEnabled && !runtimeSettings.CodexRequestCompression {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process runtime did not converge: rpm=%d settings=%+v", limiter.GetRPM(), proxy.CurrentRuntimeSettings())
}

func TestApplyRuntimeSystemSettingsValidatesBeforePublishing(t *testing.T) {
	previousRuntime := proxy.CurrentRuntimeSettings()
	t.Cleanup(func() { proxy.ApplyRuntimeSettings(previousRuntime) })
	limiter := proxy.NewRateLimiter(12)
	t.Cleanup(limiter.GetEnhancedLimiter().Stop)
	settings := &database.SystemSettings{
		GlobalRPM:             99,
		BillingTierPolicy:     proxy.BillingTierPolicyRequested,
		ModelPricingOverrides: `{not-json}`,
	}

	if _, err := applyRuntimeSystemSettings(context.Background(), settings, nil, limiter); err == nil {
		t.Fatal("invalid pricing overrides unexpectedly succeeded")
	}
	if limiter.GetRPM() != 12 {
		t.Fatalf("rate limiter changed before validation: %d", limiter.GetRPM())
	}
	if got := proxy.CurrentRuntimeSettings(); !reflect.DeepEqual(got, previousRuntime) {
		t.Fatalf("proxy runtime changed before validation: got %+v want %+v", got, previousRuntime)
	}
}
