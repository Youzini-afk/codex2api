package admin

import (
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
)

func TestBuildAccountResponsePrefersCanonicalAccountIdentity(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2})
	handler := &Handler{store: store}
	now := time.Now()
	row := &database.AccountRow{
		ID:   42,
		Name: "builder-contract",
		Credentials: map[string]interface{}{
			"access_token":       "at-builder-contract",
			"chatgpt_account_id": "workspace-preferred",
			"account_id":         "workspace-legacy",
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
}
