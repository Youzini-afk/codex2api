package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
)

// applyRuntimeSystemSettings publishes the process-owned projections of one
// committed system_settings snapshot. It is safe to call repeatedly: every
// target is a replace/update operation, and validation happens before writes.
func applyRuntimeSystemSettings(
	ctx context.Context,
	settings *database.SystemSettings,
	db *database.DB,
	rateLimiter *proxy.RateLimiter,
) (proxy.RuntimeSettings, error) {
	if settings == nil {
		return proxy.CurrentRuntimeSettings(), fmt.Errorf("system settings snapshot is nil")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return proxy.CurrentRuntimeSettings(), err
		}
	}

	pricingOverrides, err := database.ParseModelPricingOverridesJSON(settings.ModelPricingOverrides)
	if err != nil {
		return proxy.CurrentRuntimeSettings(), fmt.Errorf("parse model pricing overrides: %w", err)
	}

	effective := *settings
	if envPolicy := strings.TrimSpace(os.Getenv("CODEX_BILLING_TIER_POLICY")); envPolicy != "" {
		effective.BillingTierPolicy = proxy.NormalizeBillingTierPolicy(envPolicy)
	}

	if db != nil {
		if effective.PgMaxConns > 0 {
			db.SetMaxOpenConns(effective.PgMaxConns)
		}
		db.SetUsageLogConfig(
			effective.UsageLogMode,
			effective.UsageLogBatchSize,
			effective.UsageLogFlushIntervalSeconds,
		)
	}
	database.SetModelPricingOverrides(pricingOverrides)
	runtimeSettings := proxy.ApplyRuntimeSettingsFromSystem(&effective)
	if rateLimiter != nil {
		rateLimiter.UpdateRPM(effective.GlobalRPM)
	}
	return runtimeSettings, nil
}

func systemSettingsApplyHook(db *database.DB, rateLimiter *proxy.RateLimiter) func(context.Context, *database.SystemSettings) error {
	return func(ctx context.Context, settings *database.SystemSettings) error {
		_, err := applyRuntimeSystemSettings(ctx, settings, db, rateLimiter)
		return err
	}
}
