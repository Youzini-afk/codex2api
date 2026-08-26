package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSchedulerOutboxRoundTripAndCleanup(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "scheduler-outbox.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.InsertSchedulerOutboxEvent(ctx, SchedulerEntityAccount, 41, "upsert"); err != nil {
		t.Fatalf("InsertSchedulerOutboxEvent(account): %v", err)
	}
	firstHigh, err := db.SchedulerOutboxHighWatermark(ctx)
	if err != nil || firstHigh <= 0 {
		t.Fatalf("SchedulerOutboxHighWatermark = %d, err=%v", firstHigh, err)
	}
	if err := db.InsertSchedulerOutboxEvent(ctx, SchedulerEntityAPIKey, 7, "routing_changed"); err != nil {
		t.Fatalf("InsertSchedulerOutboxEvent(api_key): %v", err)
	}

	events, err := db.ListSchedulerOutboxEventsAfter(ctx, firstHigh, 100)
	if err != nil {
		t.Fatalf("ListSchedulerOutboxEventsAfter: %v", err)
	}
	if len(events) != 1 || events[0].EntityType != SchedulerEntityAPIKey || events[0].EntityID != 7 {
		t.Fatalf("events = %+v, want api_key/7", events)
	}

	if _, err := db.conn.ExecContext(ctx, `UPDATE scheduler_outbox SET created_at=$1`, sqliteTimeParam(time.Now().Add(-8*24*time.Hour))); err != nil {
		t.Fatalf("age scheduler outbox: %v", err)
	}
	removed, err := db.CleanupSchedulerOutbox(ctx, time.Now().Add(-7*24*time.Hour), 10000)
	if err != nil {
		t.Fatalf("CleanupSchedulerOutbox: %v", err)
	}
	if removed != 2 {
		t.Fatalf("CleanupSchedulerOutbox removed %d rows, want 2", removed)
	}
}

func TestSchedulerOutboxReplayWatermark(t *testing.T) {
	cutoff := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		timestamps []time.Time
		wantIndex  int
	}{
		{name: "empty", wantIndex: -1},
		{name: "only replay-window event", timestamps: []time.Time{cutoff.Add(time.Second)}, wantIndex: -1},
		{name: "exact boundary is replayed", timestamps: []time.Time{cutoff}, wantIndex: -1},
		{
			name: "old events advance but boundary and window do not",
			timestamps: []time.Time{
				cutoff.Add(-2 * time.Second),
				cutoff.Add(-time.Second),
				cutoff,
				cutoff.Add(time.Second),
			},
			wantIndex: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, err := New("sqlite", filepath.Join(t.TempDir(), "scheduler-replay-watermark.db"))
			if err != nil {
				t.Fatalf("New(sqlite): %v", err)
			}
			defer db.Close()
			ctx := context.Background()

			ids := make([]int64, 0, len(tc.timestamps))
			for i, createdAt := range tc.timestamps {
				if err := db.InsertSchedulerOutboxEvent(ctx, SchedulerEntityAccount, int64(i+1), "upsert"); err != nil {
					t.Fatalf("InsertSchedulerOutboxEvent(%d): %v", i, err)
				}
				id, err := db.SchedulerOutboxHighWatermark(ctx)
				if err != nil {
					t.Fatalf("SchedulerOutboxHighWatermark(%d): %v", i, err)
				}
				if _, err := db.conn.ExecContext(ctx, `UPDATE scheduler_outbox SET created_at=$1 WHERE id=$2`, db.timeArg(createdAt), id); err != nil {
					t.Fatalf("set event %d created_at: %v", id, err)
				}
				ids = append(ids, id)
			}

			watermark, err := db.SchedulerOutboxReplayWatermark(ctx, cutoff)
			if err != nil {
				t.Fatalf("SchedulerOutboxReplayWatermark: %v", err)
			}
			want := int64(0)
			if tc.wantIndex >= 0 {
				want = ids[tc.wantIndex]
			}
			if watermark != want {
				t.Fatalf("watermark = %d, want %d", watermark, want)
			}
		})
	}
}

func TestSchedulerOutboxEventRollbackIsAtomic(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "scheduler-outbox-rollback.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	err = db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if err := insertSchedulerOutboxEventTx(ctx, tx, SchedulerEntityAccount, 99, "upsert"); err != nil {
			return err
		}
		return errors.New("rollback")
	})
	if err == nil {
		t.Fatal("withWriteTx returned nil, want rollback error")
	}
	watermark, err := db.SchedulerOutboxHighWatermark(ctx)
	if err != nil {
		t.Fatalf("SchedulerOutboxHighWatermark: %v", err)
	}
	if watermark != 0 {
		t.Fatalf("watermark = %d, want 0 after rollback", watermark)
	}
}

func TestSchedulerOutboxTriggersExcludeAPIKeyUsageOnlyUpdates(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "scheduler-outbox-triggers.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	keyID, err := db.InsertAPIKey(ctx, "routing-key", "sk-routing")
	if err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	createdAt, err := db.SchedulerOutboxHighWatermark(ctx)
	if err != nil || createdAt == 0 {
		t.Fatalf("watermark after key insert = %d, err=%v", createdAt, err)
	}
	if err := db.UpdateAPIKeyQuotaLimit(ctx, keyID, 10); err != nil {
		t.Fatalf("UpdateAPIKeyQuotaLimit: %v", err)
	}
	afterQuota, err := db.SchedulerOutboxHighWatermark(ctx)
	if err != nil {
		t.Fatalf("watermark after quota update: %v", err)
	}
	if afterQuota != createdAt {
		t.Fatalf("usage-only API key update emitted routing event: %d -> %d", createdAt, afterQuota)
	}

	if err := db.UpdateAPIKeyAllowedGroups(ctx, keyID, []int64{9}); err != nil {
		t.Fatalf("UpdateAPIKeyAllowedGroups: %v", err)
	}
	events, err := db.ListSchedulerOutboxEventsAfter(ctx, createdAt, 10)
	if err != nil {
		t.Fatalf("ListSchedulerOutboxEventsAfter: %v", err)
	}
	if len(events) != 1 || events[0].EntityType != SchedulerEntityAPIKey || events[0].EntityID != keyID {
		t.Fatalf("routing events = %+v, want api_key/%d", events, keyID)
	}
}

func TestSchedulerOutboxTriggersExcludeAccountUsageSnapshots(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "scheduler-account-trigger.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	accountID, err := db.InsertAccount(ctx, "snapshot-account", "rt", "")
	if err != nil {
		t.Fatalf("InsertAccount: %v", err)
	}
	createdAt, err := db.SchedulerOutboxHighWatermark(ctx)
	if err != nil {
		t.Fatalf("SchedulerOutboxHighWatermark: %v", err)
	}
	if err := db.UpdateCredentials(ctx, accountID, map[string]interface{}{
		"codex_7d_used_percent":  42,
		"codex_usage_updated_at": time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("UpdateCredentials(usage): %v", err)
	}
	afterUsage, err := db.SchedulerOutboxHighWatermark(ctx)
	if err != nil {
		t.Fatalf("watermark after usage: %v", err)
	}
	if afterUsage != createdAt {
		t.Fatalf("usage snapshot emitted routing event: %d -> %d", createdAt, afterUsage)
	}
	if err := db.UpdateCredentials(ctx, accountID, map[string]interface{}{"access_token": "rotated"}); err != nil {
		t.Fatalf("UpdateCredentials(access token): %v", err)
	}
	afterToken, err := db.SchedulerOutboxHighWatermark(ctx)
	if err != nil || afterToken <= afterUsage {
		t.Fatalf("routing credential update watermark = %d, previous=%d, err=%v", afterToken, afterUsage, err)
	}
}

func TestSchedulerOutboxTriggersAccountReserveChanges(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "scheduler-account-reserve-trigger.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	accountID, err := db.InsertAccount(ctx, "reserve-account", "rt", "")
	if err != nil {
		t.Fatalf("InsertAccount: %v", err)
	}
	watermark, err := db.SchedulerOutboxHighWatermark(ctx)
	if err != nil {
		t.Fatalf("SchedulerOutboxHighWatermark: %v", err)
	}

	set5h := OptionalNullInt64{Set: true, Value: sql.NullInt64{Int64: 11, Valid: true}}
	set7d := OptionalNullInt64{Set: true, Value: sql.NullInt64{Int64: 23, Valid: true}}
	if err := db.UpdateAccountSchedulerMetadata(ctx, accountID, OptionalNullInt64{}, OptionalNullInt64{}, set5h, set7d, OptionalBool{}, OptionalInt64Slice{}, OptionalStringSlice{}, OptionalInt64Slice{}, OptionalString{}, nil); err != nil {
		t.Fatalf("UpdateAccountSchedulerMetadata(set reserves): %v", err)
	}
	afterSet, err := db.SchedulerOutboxHighWatermark(ctx)
	if err != nil || afterSet != watermark+1 {
		t.Fatalf("reserve set watermark = %d, want %d, err=%v", afterSet, watermark+1, err)
	}

	clear5h := OptionalNullInt64{Set: true, Value: sql.NullInt64{}}
	clear7d := OptionalNullInt64{Set: true, Value: sql.NullInt64{}}
	if err := db.UpdateAccountSchedulerMetadata(ctx, accountID, OptionalNullInt64{}, OptionalNullInt64{}, clear5h, clear7d, OptionalBool{}, OptionalInt64Slice{}, OptionalStringSlice{}, OptionalInt64Slice{}, OptionalString{}, nil); err != nil {
		t.Fatalf("UpdateAccountSchedulerMetadata(clear reserves): %v", err)
	}
	afterClear, err := db.SchedulerOutboxHighWatermark(ctx)
	if err != nil || afterClear != afterSet+1 {
		t.Fatalf("reserve clear watermark = %d, want %d, err=%v", afterClear, afterSet+1, err)
	}
}

func TestSchedulerOutboxTriggersAntigravitySchedulingFactsOnly(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "scheduler-antigravity-trigger.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	accountID, err := db.InsertAccountWithCredentials(ctx, "antigravity-account", map[string]interface{}{
		"upstream_type": UpstreamChannelAntigravity,
		"access_token":  "access",
		"project_id":    "project-a",
	}, "")
	if err != nil {
		t.Fatalf("InsertAccountWithCredentials: %v", err)
	}
	watermark, err := db.SchedulerOutboxHighWatermark(ctx)
	if err != nil {
		t.Fatalf("SchedulerOutboxHighWatermark: %v", err)
	}

	update := func(label string, fields map[string]interface{}, wantEvent bool) {
		t.Helper()
		before := watermark
		if err := db.UpdateCredentials(ctx, accountID, fields); err != nil {
			t.Fatalf("%s: UpdateCredentials: %v", label, err)
		}
		var err error
		watermark, err = db.SchedulerOutboxHighWatermark(ctx)
		if err != nil {
			t.Fatalf("%s: SchedulerOutboxHighWatermark: %v", label, err)
		}
		want := before
		if wantEvent {
			want++
		}
		if watermark != want {
			t.Fatalf("%s: watermark = %d, want %d", label, watermark, want)
		}
	}

	update("project change", map[string]interface{}{"project_id": "project-b"}, true)
	update("healthy quota snapshot", map[string]interface{}{
		"antigravity_quota": `{"models":[{"model_id":"gemini","remaining_percent":80}],"updated_at":"2026-08-27T00:00:00Z"}`,
	}, false)
	update("quota usage-only refresh", map[string]interface{}{
		"antigravity_quota": `{"models":[{"model_id":"gemini","remaining_percent":60}],"updated_at":"2026-08-27T00:05:00Z"}`,
	}, false)
	update("quota forbidden", map[string]interface{}{
		"antigravity_quota": `{"models":[],"forbidden":true,"updated_at":"2026-08-27T00:06:00Z"}`,
	}, true)
	update("quota forbidden refresh", map[string]interface{}{
		"antigravity_quota": `{"models":[],"forbidden":true,"updated_at":"2026-08-27T00:07:00Z"}`,
	}, false)
	update("quota recovered", map[string]interface{}{
		"antigravity_quota": `{"models":[{"model_id":"gemini","remaining_percent":100}],"updated_at":"2026-08-27T00:08:00Z"}`,
	}, true)

	update("permission denied", map[string]interface{}{
		"antigravity_permissions": `{"allowed":false,"reason":"policy denied","updated_at":"2026-08-27T00:09:00Z"}`,
	}, true)
	update("permission denial observation refresh", map[string]interface{}{
		"antigravity_permissions": `{"allowed":false,"reason":"new wording","updated_at":"2026-08-27T00:10:00Z"}`,
	}, false)
	update("permission recovered", map[string]interface{}{
		"antigravity_permissions": `{"allowed":true,"updated_at":"2026-08-27T00:11:00Z"}`,
	}, true)
	update("observed permission missing allowed", map[string]interface{}{
		"antigravity_permissions": `{"reason":"account restricted","updated_at":"2026-08-27T00:11:30Z"}`,
	}, true)
	update("missing-allowed permission recovered", map[string]interface{}{
		"antigravity_permissions": `{"allowed":true,"updated_at":"2026-08-27T00:11:45Z"}`,
	}, true)
	update("legacy entitlement denied", map[string]interface{}{
		"antigravity_permissions":  "",
		"antigravity_entitlements": `{"allowed":false,"reason":"legacy denied","updated_at":"2026-08-27T00:12:00Z"}`,
	}, true)
	update("legacy entitlement recovered", map[string]interface{}{
		"antigravity_entitlements": `{"allowed":true,"updated_at":"2026-08-27T00:13:00Z"}`,
	}, true)

	const permanent = "Antigravity token refresh failed: invalid_grant"
	update("transient sync error", map[string]interface{}{"antigravity_sync_error": permanent}, false)
	update("permanent refresh fence", map[string]interface{}{"antigravity_permanent_refresh_error": permanent}, true)
	update("permanent refresh fence cleared", map[string]interface{}{"antigravity_permanent_refresh_error": ""}, true)
	update("identity revalidation fence", map[string]interface{}{
		"antigravity_sync_error": "Antigravity credential rotated before Google identity could be reverified: userinfo unavailable",
	}, true)
	update("identity revalidation recovered", map[string]interface{}{"antigravity_sync_error": ""}, true)
}

func TestSchedulerBatchAccountProjectionHelpers(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "scheduler-batch-projection.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	firstID, err := db.InsertAccount(ctx, "batch-first", "rt-first", "")
	if err != nil {
		t.Fatalf("InsertAccount(first): %v", err)
	}
	secondID, err := db.InsertAccount(ctx, "batch-second", "rt-second", "")
	if err != nil {
		t.Fatalf("InsertAccount(second): %v", err)
	}
	firstGroup, err := db.CreateAccountGroup(ctx, "batch-group-a", "", "", 0, 0, sql.NullInt64{})
	if err != nil {
		t.Fatalf("CreateAccountGroup(first): %v", err)
	}
	secondGroup, err := db.CreateAccountGroup(ctx, "batch-group-b", "", "", 0, 0, sql.NullInt64{})
	if err != nil {
		t.Fatalf("CreateAccountGroup(second): %v", err)
	}
	if err := db.SetAccountGroups(ctx, firstID, []int64{secondGroup, firstGroup}); err != nil {
		t.Fatalf("SetAccountGroups(first): %v", err)
	}
	if err := db.SetAccountGroups(ctx, secondID, []int64{secondGroup}); err != nil {
		t.Fatalf("SetAccountGroups(second): %v", err)
	}
	if err := db.SetModelCooldown(ctx, firstID, "gpt-5.6", "rate_limited", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("SetModelCooldown(active): %v", err)
	}
	if err := db.SetModelCooldown(ctx, secondID, "expired", "rate_limited", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("SetModelCooldown(expired): %v", err)
	}

	memberships, err := db.ListAccountGroupMembershipsByAccountIDs(ctx, []int64{secondID, firstID, firstID, 0, 999999})
	if err != nil {
		t.Fatalf("ListAccountGroupMembershipsByAccountIDs: %v", err)
	}
	if got := memberships[firstID]; len(got) != 2 || got[0] != firstGroup || got[1] != secondGroup {
		t.Fatalf("first memberships = %v, want [%d %d]", got, firstGroup, secondGroup)
	}
	if got := memberships[secondID]; len(got) != 1 || got[0] != secondGroup {
		t.Fatalf("second memberships = %v, want [%d]", got, secondGroup)
	}

	cooldowns, err := db.ListActiveModelCooldownsForAccounts(ctx, []int64{secondID, firstID, firstID, 0, 999999})
	if err != nil {
		t.Fatalf("ListActiveModelCooldownsForAccounts: %v", err)
	}
	if len(cooldowns) != 1 || cooldowns[0].AccountID != firstID || cooldowns[0].Model != "gpt-5.6" {
		t.Fatalf("active cooldowns = %+v, want first account gpt-5.6 only", cooldowns)
	}
}

func TestCleanupSchedulerOutboxThroughRespectsWatermark(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "scheduler-outbox-through.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := db.InsertSchedulerOutboxEvent(ctx, SchedulerEntityAccount, int64(100+i), "upsert"); err != nil {
			t.Fatalf("InsertSchedulerOutboxEvent(%d): %v", i, err)
		}
	}
	if _, err := db.conn.ExecContext(ctx, `UPDATE scheduler_outbox SET created_at=$1`, sqliteTimeParam(time.Now().Add(-8*24*time.Hour))); err != nil {
		t.Fatalf("age scheduler outbox: %v", err)
	}

	// 只清到本实例已消费的水位:第 3 条虽然过期但未消费,必须保留。
	removed, err := db.CleanupSchedulerOutboxThrough(ctx, time.Now().Add(-7*24*time.Hour), 2, 10000)
	if err != nil {
		t.Fatalf("CleanupSchedulerOutboxThrough: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	events, err := db.ListSchedulerOutboxEventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatalf("ListSchedulerOutboxEventsAfter: %v", err)
	}
	if len(events) != 1 || events[0].ID != 3 {
		t.Fatalf("surviving events = %+v, want only id 3", events)
	}
}

func TestListSchedulerOutboxEventsByIDs(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "scheduler-outbox-byids.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := db.InsertSchedulerOutboxEvent(ctx, SchedulerEntityAccount, int64(200+i), "upsert"); err != nil {
			t.Fatalf("InsertSchedulerOutboxEvent(%d): %v", i, err)
		}
	}
	events, err := db.ListSchedulerOutboxEventsByIDs(ctx, []int64{1, 3, 999})
	if err != nil {
		t.Fatalf("ListSchedulerOutboxEventsByIDs: %v", err)
	}
	if len(events) != 2 || events[0].ID != 1 || events[1].ID != 3 {
		t.Fatalf("events = %+v, want ids 1 and 3", events)
	}
}

func TestPostgresSchedulerOutboxReplayWatermarkCoversOutOfOrderCommit(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CODEX2API_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("CODEX2API_TEST_POSTGRES_DSN is not set")
	}
	schema := fmt.Sprintf("scheduler_outbox_order_%d", time.Now().UnixNano())
	cleanupPostgresMigrationSchema(t, dsn, schema)

	db, err := New("postgres", dsn, schema)
	if err != nil {
		t.Fatalf("New(postgres): %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		cleanupPostgresMigrationSchema(t, dsn, schema)
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if err := db.InsertSchedulerOutboxEvent(ctx, SchedulerEntityAccount, 1, "baseline"); err != nil {
		t.Fatalf("insert baseline: %v", err)
	}
	baselineID, err := db.SchedulerOutboxHighWatermark(ctx)
	if err != nil {
		t.Fatalf("baseline high watermark: %v", err)
	}
	cutoff := time.Now().Add(-10 * time.Minute)
	if _, err := db.conn.ExecContext(ctx, `UPDATE scheduler_outbox SET created_at=$1 WHERE id=$2`, cutoff.Add(-time.Minute), baselineID); err != nil {
		t.Fatalf("age baseline: %v", err)
	}

	txA, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin transaction A: %v", err)
	}
	defer txA.Rollback()
	var idA int64
	if err := txA.QueryRowContext(ctx, `INSERT INTO scheduler_outbox(entity_type,entity_id,event_type,created_at) VALUES($1,$2,$3,CURRENT_TIMESTAMP) RETURNING id`, SchedulerEntityAccount, 2, "late").Scan(&idA); err != nil {
		t.Fatalf("insert transaction A: %v", err)
	}

	txB, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin transaction B: %v", err)
	}
	var idB int64
	if err := txB.QueryRowContext(ctx, `INSERT INTO scheduler_outbox(entity_type,entity_id,event_type,created_at) VALUES($1,$2,$3,CURRENT_TIMESTAMP) RETURNING id`, SchedulerEntityAccount, 3, "early").Scan(&idB); err != nil {
		_ = txB.Rollback()
		t.Fatalf("insert transaction B: %v", err)
	}
	if err := txB.Commit(); err != nil {
		t.Fatalf("commit transaction B: %v", err)
	}
	if idB <= idA {
		t.Fatalf("allocated ids A=%d B=%d, want B>A", idA, idB)
	}
	high, err := db.SchedulerOutboxHighWatermark(ctx)
	if err != nil || high != idB {
		t.Fatalf("visible high watermark = %d, want %d, err=%v", high, idB, err)
	}
	replayWatermark, err := db.SchedulerOutboxReplayWatermark(ctx, cutoff)
	if err != nil || replayWatermark != baselineID {
		t.Fatalf("replay watermark = %d, want baseline %d, err=%v", replayWatermark, baselineID, err)
	}

	if err := txA.Commit(); err != nil {
		t.Fatalf("commit transaction A: %v", err)
	}
	events, err := db.ListSchedulerOutboxEventsAfter(ctx, replayWatermark, 10)
	if err != nil {
		t.Fatalf("ListSchedulerOutboxEventsAfter: %v", err)
	}
	if len(events) != 2 || events[0].ID != idA || events[1].ID != idB {
		t.Fatalf("replayed ids = %+v, want [%d %d]", events, idA, idB)
	}
}
