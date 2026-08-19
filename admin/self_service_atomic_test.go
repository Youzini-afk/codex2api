package admin

import (
	"context"
	"strings"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
)

func TestUpsertSelfServiceAccountAtomicallyPersistsNewAccountDefaults(t *testing.T) {
	db := newTestAdminDB(t)
	store := auth.NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 2})
	store.SetCodexFingerprintDefaultMode(auth.CodexFingerprintModeSession)
	handler := &Handler{db: db, store: store}

	id, err := handler.upsertSelfServiceAccount(context.Background(), "pending@example.com", "", tokenCredentialSeed{
		refreshToken: "rt-self-service-atomic",
		accessToken:  "at-self-service-atomic",
		email:        "pending@example.com",
		accountID:    "workspace-self-service",
		workspaceID:  "workspace-self-service",
	}, "contact@example.com")
	if err != nil {
		t.Fatalf("upsertSelfServiceAccount: %v", err)
	}

	row, err := db.GetAccountByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetAccountByID: %v", err)
	}
	if row.Enabled {
		t.Fatal("pending self-service account must be disabled")
	}
	if !strings.Contains(row.Note, "contact@example.com") {
		t.Fatalf("note = %q, want contact email", row.Note)
	}
	if len(row.Tags) != 1 || row.Tags[0] != selfServiceTag {
		t.Fatalf("tags = %#v, want [%q]", row.Tags, selfServiceTag)
	}
	if got := row.GetCredential(auth.CodexFingerprintModeCredentialKey); got != auth.CodexFingerprintModeSession {
		t.Fatalf("codex fingerprint mode = %q, want %q", got, auth.CodexFingerprintModeSession)
	}
	if handler.store.FindByID(id) != nil {
		t.Fatal("pending self-service account must not enter the runtime pool")
	}
}
