package admin

import (
	"database/sql"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
)

func TestBuildAccountResponsePreservesForkAccountContracts(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2})
	store.SetUsageProbeMaxAge(time.Minute)
	store.SetModelCooldownSettings(database.ModelCooldownSettings{
		RelayMode:           database.ModelCooldownModeOff,
		RelaySeconds:        2,
		RelayBackoffEnabled: false,
		OAuthMode:           database.ModelCooldownModeAdaptive,
		OAuthSeconds:        300,
		OAuthBackoffEnabled: true,
	})
	handler := &Handler{store: store}
	now := time.Now()
	row := &database.AccountRow{
		ID:   42,
		Name: "builder-contract",
		Credentials: map[string]interface{}{
			"access_token":                    "at-builder-contract",
			"chatgpt_account_id":              "workspace-preferred",
			"account_id":                      "workspace-legacy",
			"model_cooldown_mode_override":    database.ModelCooldownModeFixed,
			"model_cooldown_seconds_override": float64(45),
			"model_cooldown_backoff_override": false,
		},
		Status:                "active",
		Enabled:               false,
		UsageReservePercent5h: sql.NullInt64{Int64: 10, Valid: true},
		UsageReservePercent7d: sql.NullInt64{Int64: 20, Valid: true},
		CreatedAt:             now,
		UpdatedAt:             now,
	}

	resp := handler.buildAccountResponse(row, nil, nil, nil, nil, true)
	if resp.ChatGPTAccountID != "workspace-preferred" {
		t.Fatalf("ChatGPTAccountID = %q, want workspace-preferred", resp.ChatGPTAccountID)
	}
	if resp.UsageReservePercent5h == nil || *resp.UsageReservePercent5h != 10 || resp.UsageReservePercent7d == nil || *resp.UsageReservePercent7d != 20 {
		t.Fatalf("usage reserve = (%v, %v), want (10, 20)", resp.UsageReservePercent5h, resp.UsageReservePercent7d)
	}
	if resp.UsageReserveActiveWindows == nil || len(resp.UsageReserveActiveWindows) != 0 {
		t.Fatalf("UsageReserveActiveWindows = %#v, want non-nil empty slice", resp.UsageReserveActiveWindows)
	}
	if resp.ModelCooldownModeOverride == nil || *resp.ModelCooldownModeOverride != database.ModelCooldownModeFixed ||
		resp.ModelCooldownSecondsOverride == nil || *resp.ModelCooldownSecondsOverride != 45 ||
		resp.ModelCooldownBackoffOverride == nil || *resp.ModelCooldownBackoffOverride ||
		resp.ModelCooldownModeEffective != database.ModelCooldownModeFixed ||
		resp.ModelCooldownSecondsEffective != 45 || resp.ModelCooldownBackoffEffective {
		t.Fatalf("non-runtime cooldown contract lost: %+v", resp)
	}
}

func TestBuildAccountResponseUsesConfiguredUsageProbeMaxAge(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2})
	store.SetUsageProbeMaxAge(time.Minute)
	handler := &Handler{store: store}
	now := time.Now()
	reserve := int64(10)
	runtimeAccount := &auth.Account{
		DBID:                  43,
		AccessToken:           "at-builder-runtime",
		Status:                auth.StatusReady,
		UsageReservePercent5h: &reserve,
	}
	runtimeAccount.SetUsageSnapshot5hAt(95, now.Add(time.Hour), now.Add(-5*time.Minute))
	if got := runtimeAccount.RuntimeStatus(); got != "usage_reserved" {
		t.Fatalf("fixture RuntimeStatus() = %q, want usage_reserved under the default max age", got)
	}

	row := &database.AccountRow{
		ID:                    43,
		Name:                  "builder-runtime",
		Credentials:           map[string]interface{}{"access_token": "at-builder-runtime"},
		Status:                "active",
		Enabled:               true,
		UsageReservePercent5h: sql.NullInt64{Int64: reserve, Valid: true},
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	resp := handler.buildAccountResponse(row, runtimeAccount, nil, nil, nil, false)
	if resp.Status != "active" {
		t.Fatalf("Status = %q, want active with the configured one-minute max age", resp.Status)
	}
	if len(resp.UsageReserveActiveWindows) != 0 {
		t.Fatalf("UsageReserveActiveWindows = %#v, want empty for stale usage", resp.UsageReserveActiveWindows)
	}
}
