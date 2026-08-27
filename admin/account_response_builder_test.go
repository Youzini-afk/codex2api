package admin

import (
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
		Status:    "active",
		Enabled:   false,
		CreatedAt: now,
		UpdatedAt: now,
	}

	resp := handler.buildAccountResponse(row, nil, nil, nil, nil, true)
	if resp.ChatGPTAccountID != "workspace-preferred" {
		t.Fatalf("ChatGPTAccountID = %q, want workspace-preferred", resp.ChatGPTAccountID)
	}
	if resp.ModelCooldownModeOverride == nil || *resp.ModelCooldownModeOverride != database.ModelCooldownModeFixed ||
		resp.ModelCooldownSecondsOverride == nil || *resp.ModelCooldownSecondsOverride != 45 ||
		resp.ModelCooldownBackoffOverride == nil || *resp.ModelCooldownBackoffOverride ||
		resp.ModelCooldownModeEffective != database.ModelCooldownModeFixed ||
		resp.ModelCooldownSecondsEffective != 45 || resp.ModelCooldownBackoffEffective {
		t.Fatalf("non-runtime cooldown contract lost: %+v", resp)
	}
}
