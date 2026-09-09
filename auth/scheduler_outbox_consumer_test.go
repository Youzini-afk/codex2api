package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/database"
)

func waitForSchedulerProjection(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("scheduler projection did not converge before timeout")
}

func systemSettingsSyncFixture() *database.SystemSettings {
	return &database.SystemSettings{
		MaxConcurrency:                     2,
		GlobalRPM:                          0,
		TestModel:                          DefaultTestModel,
		TestContent:                        DefaultTestContent,
		TestConcurrency:                    4,
		BackgroundRefreshIntervalMinutes:   2,
		UsageProbeMaxAgeMinutes:            10,
		UsageProbeConcurrency:              4,
		UsageProbeResponsesFallbackEnabled: true,
		RecoveryProbeIntervalMinutes:       30,
		SchedulerMode:                      "round_robin",
		AffinityMode:                       AffinityModeBounded,
		SchedulerEngine:                    "legacy",
		CodexRequestCompression:            true,
		CodexFastModelAliasEnabled:         true,
		CodexReasoningEffortAliasEnabled:   true,
		CodexWSHideUpstreamErrors:          true,
		CodexWSSilentRetryEnabled:          true,
		CodexWSSilentMaxRetries:            2,
		CodexWSSizeRouterEnabled:           true,
		CodexWSBusyAcquireMaxWaitSec:       30,
		CodexWSBusyPatienceSec:             2,
		CodexWSStatelessSlots:              8,
		CodexContinueMaxRounds:             8,
		CodexCLIVersionSyncEnabled:         true,
		CodexCLIVersionSyncIntervalHours:   12,
		SessionSlotBufferSeconds:           10,
		SmartPacingMinConcurrency:          1,
		SmartPacingWindows:                 "5h,7d",
	}
}

func TestStoreInitReloadsSettingsCommittedAfterBootstrapRead(t *testing.T) {
	t.Setenv("CODEX_SCHEDULER_ENGINE", "")
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "settings-startup-window.db"))
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	initial := systemSettingsSyncFixture()
	if err := db.UpdateSystemSettings(ctx, initial); err != nil {
		t.Fatalf("seed initial settings: %v", err)
	}
	bootstrapSnapshot, err := db.GetSystemSettings(ctx)
	if err != nil {
		t.Fatalf("bootstrap GetSystemSettings: %v", err)
	}
	store := NewStore(db, nil, bootstrapSnapshot)
	t.Cleanup(func() {
		store.Stop()
		_ = db.Close()
	})

	// This commit represents another replica updating settings after main read
	// its bootstrap snapshot but before Store.Init captured the outbox watermark.
	updated := *bootstrapSnapshot
	updated.MaxConcurrency = 9
	updated.GlobalRPM = 73
	updated.SchedulerEngine = "indexed"
	updated.SchedulerMode = "fill_first"
	updated.SessionSlotBufferEnabled = true
	updated.SessionSlotBufferSeconds = 17
	updated.CodexFastModelAliasEnabled = false
	updated.CodexReasoningEffortAliasEnabled = false
	updated.CodexFastTierInterceptEnabled = true
	updated.CodexRequestCompression = false
	if err := db.UpdateSystemSettings(ctx, &updated); err != nil {
		t.Fatalf("commit settings in startup window: %v", err)
	}

	var hookSnapshot atomic.Pointer[database.SystemSettings]
	store.SetSystemSettingsApplyHook(func(_ context.Context, settings *database.SystemSettings) error {
		copy := *settings
		hookSnapshot.Store(&copy)
		return nil
	})
	if err := store.Init(ctx); err != nil {
		t.Fatalf("Store.Init: %v", err)
	}

	if store.GetMaxConcurrency() != 9 || store.GetSchedulerMode() != "fill_first" || store.SchedulerEngine() != "indexed" {
		t.Fatalf("Store settings = concurrency:%d mode:%s engine:%s", store.GetMaxConcurrency(), store.GetSchedulerMode(), store.SchedulerEngine())
	}
	if !store.SessionSlotBufferEnabled() || store.GetSessionSlotBuffer() != 17*time.Second {
		t.Fatalf("session slot buffer = enabled:%t duration:%s", store.SessionSlotBufferEnabled(), store.GetSessionSlotBuffer())
	}
	if store.CodexFastModelAliasEnabled() || store.CodexReasoningEffortAliasEnabled() || !store.CodexFastTierInterceptEnabled() || store.CodexRequestCompression() {
		t.Fatalf("Codex runtime flags did not converge from committed snapshot")
	}
	applied := hookSnapshot.Load()
	if applied == nil || applied.GlobalRPM != 73 || applied.MaxConcurrency != 9 {
		t.Fatalf("external hook snapshot = %+v", applied)
	}
}

func TestStoreInitReplaysSettingsCommittedAfterInitSnapshot(t *testing.T) {
	t.Setenv("CODEX_SCHEDULER_ENGINE", "")
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "settings-init-snapshot-window.db"))
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	initial := systemSettingsSyncFixture()
	if err := db.UpdateSystemSettings(ctx, initial); err != nil {
		t.Fatalf("seed initial settings: %v", err)
	}
	store := NewStore(db, nil, initial)
	t.Cleanup(func() {
		store.Stop()
		_ = db.Close()
	})

	updated := *initial
	updated.MaxConcurrency = 17
	updated.SchedulerEngine = "indexed"
	updated.SchedulerMode = "fill_first"
	updated.SessionSlotBufferEnabled = true
	updated.SessionSlotBufferSeconds = 23
	var hookCalls atomic.Int32
	store.SetSystemSettingsApplyHook(func(_ context.Context, settings *database.SystemSettings) error {
		if hookCalls.Add(1) != 1 {
			return nil
		}
		if settings.MaxConcurrency != initial.MaxConcurrency {
			return fmt.Errorf("first init snapshot concurrency=%d, want %d", settings.MaxConcurrency, initial.MaxConcurrency)
		}
		// Store.Init has already read and published its settings snapshot here,
		// but the account snapshot and consumer startup have not run yet. The
		// update therefore models another replica committing inside that window.
		return db.UpdateSystemSettings(ctx, &updated)
	})

	if err := store.Init(ctx); err != nil {
		t.Fatalf("Store.Init: %v", err)
	}
	waitForSchedulerProjection(t, func() bool {
		metrics := store.GetSchedulerMetrics()
		return hookCalls.Load() >= 2 && metrics.OutboxEvents > 0 &&
			store.GetMaxConcurrency() == updated.MaxConcurrency &&
			store.SchedulerEngine() == updated.SchedulerEngine &&
			store.GetSchedulerMode() == updated.SchedulerMode &&
			store.SessionSlotBufferEnabled() &&
			store.GetSessionSlotBuffer() == 23*time.Second
	})
}

func TestSettingsEventHookFailureDoesNotAdvanceWatermark(t *testing.T) {
	t.Setenv("CODEX_SCHEDULER_ENGINE", "")
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "settings-hook-retry.db"))
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	settings := systemSettingsSyncFixture()
	if err := db.UpdateSystemSettings(ctx, settings); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	watermark, err := db.SchedulerOutboxHighWatermark(ctx)
	if err != nil {
		t.Fatalf("initial watermark: %v", err)
	}

	settings.MaxConcurrency = 11
	settings.GlobalRPM = 91
	if err := db.UpdateSystemSettings(ctx, settings); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	store := NewStore(db, nil, systemSettingsSyncFixture())
	var attempts atomic.Int32
	store.SetSystemSettingsApplyHook(func(context.Context, *database.SystemSettings) error {
		if attempts.Add(1) == 1 {
			return errors.New("transient runtime hook failure")
		}
		return nil
	})
	cursor := &schedulerOutboxCursor{watermark: watermark, holes: make(map[int64]time.Time)}
	if err := store.consumeSchedulerOutbox(ctx, cursor); err == nil {
		t.Fatal("first consume unexpectedly succeeded")
	}
	if cursor.watermark != watermark {
		t.Fatalf("watermark advanced on hook failure: got %d want %d", cursor.watermark, watermark)
	}
	if err := store.consumeSchedulerOutbox(ctx, cursor); err != nil {
		t.Fatalf("retry consume: %v", err)
	}
	if cursor.watermark <= watermark || attempts.Load() != 2 {
		t.Fatalf("retry state = watermark:%d attempts:%d", cursor.watermark, attempts.Load())
	}
	if store.GetMaxConcurrency() != 11 {
		t.Fatalf("idempotent retry did not publish settings, concurrency=%d", store.GetMaxConcurrency())
	}
}

func TestSharedDatabaseSettingsConvergeAcrossStores(t *testing.T) {
	t.Setenv("CODEX_SCHEDULER_ENGINE", "")
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "settings-two-stores.db"))
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	settings := systemSettingsSyncFixture()
	if err := db.UpdateSystemSettings(ctx, settings); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	first := NewStore(db, nil, settings)
	second := NewStore(db, nil, settings)
	t.Cleanup(func() {
		first.Stop()
		second.Stop()
		_ = db.Close()
	})
	if err := first.Init(ctx); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	if err := second.Init(ctx); err != nil {
		t.Fatalf("second Init: %v", err)
	}

	updated := *settings
	updated.MaxConcurrency = 13
	updated.SchedulerEngine = "shadow"
	updated.SessionSlotBufferEnabled = true
	updated.SessionSlotBufferSeconds = 21
	updated.CodexFastModelAliasEnabled = false
	updated.CodexReasoningEffortAliasEnabled = false
	updated.CodexRequestCompression = false
	if err := db.UpdateSystemSettings(ctx, &updated); err != nil {
		t.Fatalf("update shared settings: %v", err)
	}
	if err := db.UpdateClaudeConfig(ctx, `{"fingerprint_mode":"force","default_timezone":"Asia/Shanghai","session_window_limit":6,"cli_version_sync_enabled":false,"cli_version_sync_interval_hours":9,"first_token_timeout_seconds":45,"stream_keepalive_enabled":false}`); err != nil {
		t.Fatalf("update shared Claude settings: %v", err)
	}
	waitForSchedulerProjection(t, func() bool {
		return first.GetMaxConcurrency() == 13 && second.GetMaxConcurrency() == 13 &&
			first.SchedulerEngine() == "shadow" && second.SchedulerEngine() == "shadow" &&
			first.SessionSlotBufferEnabled() && second.SessionSlotBufferEnabled() &&
			!first.CodexFastModelAliasEnabled() && !second.CodexFastModelAliasEnabled() &&
			!first.CodexReasoningEffortAliasEnabled() && !second.CodexReasoningEffortAliasEnabled() &&
			!first.CodexRequestCompression() && !second.CodexRequestCompression() &&
			first.ClaudeFingerprintModeDefault() == ClaudeFingerprintModeForce &&
			second.ClaudeFingerprintModeDefault() == ClaudeFingerprintModeForce &&
			first.ClaudeDefaultTimezone() == "Asia/Shanghai" && second.ClaudeDefaultTimezone() == "Asia/Shanghai" &&
			first.ClaudeSessionWindowLimit() == 6 && second.ClaudeSessionWindowLimit() == 6 &&
			!first.ClaudeCLIVersionSyncEnabled() && !second.ClaudeCLIVersionSyncEnabled() &&
			first.ClaudeCLIVersionSyncIntervalHours() == 9 && second.ClaudeCLIVersionSyncIntervalHours() == 9 &&
			first.ClaudeFirstTokenTimeoutSeconds() == 45 && second.ClaudeFirstTokenTimeoutSeconds() == 45 &&
			!first.ClaudeStreamKeepaliveEnabled() && !second.ClaudeStreamKeepaliveEnabled()
	})
}

func TestReloadSystemSettingsPreservesSchedulerEnvironmentOverride(t *testing.T) {
	t.Setenv("CODEX_SCHEDULER_ENGINE", "legacy")
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "settings-scheduler-env.db"))
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	settings := systemSettingsSyncFixture()
	settings.SchedulerEngine = "indexed"
	if err := db.UpdateSystemSettings(ctx, settings); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	store := NewStore(db, nil, settings)
	if err := store.reloadSystemSettings(ctx); err != nil {
		t.Fatalf("reloadSystemSettings: %v", err)
	}
	if got := store.SchedulerEngine(); got != "legacy" {
		t.Fatalf("SchedulerEngine = %q, want env override legacy", got)
	}
}

func TestSchedulerOutboxConsumerLoadsUpdatesAndRemovesAccount(t *testing.T) {
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "scheduler-consumer.db"))
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	store := NewStore(db, nil, &database.SystemSettings{
		MaxConcurrency:  1,
		SchedulerEngine: "indexed",
	})
	t.Cleanup(func() {
		store.Stop()
		_ = db.Close()
	})
	if err := store.Init(ctx); err != nil {
		t.Fatalf("Store.Init: %v", err)
	}

	accountID, err := db.InsertOpenAIResponsesAccount(ctx, "outbox-account", map[string]interface{}{
		"upstream_type": UpstreamOpenAIResponses,
		"base_url":      "https://outbox.example",
		"api_key":       "sk-outbox",
		"models":        []string{"gpt-5.6"},
	}, "")
	if err != nil {
		t.Fatalf("InsertOpenAIResponsesAccount: %v", err)
	}
	waitForSchedulerProjection(t, func() bool { return store.FindByID(accountID) != nil })

	selected := store.Next()
	if selected == nil || selected.ID() != accountID {
		t.Fatalf("Next() = %v, want account %d", selected, accountID)
	}
	store.Release(selected)

	if err := db.SetAccountEnabled(ctx, accountID, false); err != nil {
		t.Fatalf("SetAccountEnabled(false): %v", err)
	}
	waitForSchedulerProjection(t, func() bool {
		acc := store.FindByID(accountID)
		return acc != nil && atomic.LoadInt32(&acc.DispatchPaused) != 0
	})
	if selected := store.Next(); selected != nil {
		store.Release(selected)
		t.Fatalf("Next() selected disabled account %d", selected.ID())
	}

	if err := db.SoftDeleteAccount(ctx, accountID); err != nil {
		t.Fatalf("SoftDeleteAccount: %v", err)
	}
	waitForSchedulerProjection(t, func() bool { return store.FindByID(accountID) == nil })

	metrics := store.GetSchedulerMetrics()
	if metrics.OutboxEvents < 3 || metrics.OutboxErrors != 0 {
		t.Fatalf("outbox metrics = %+v, want >=3 events and no errors", metrics)
	}
}

func TestSchedulerOutboxConsumerConvergesReserveAndAntigravityFences(t *testing.T) {
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "scheduler-antigravity-convergence.db"))
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	store := NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 2, SchedulerEngine: "indexed"})
	t.Cleanup(func() {
		store.Stop()
		_ = db.Close()
	})
	if err := store.Init(ctx); err != nil {
		t.Fatalf("Store.Init: %v", err)
	}

	accountID, err := db.InsertAccountWithCredentials(ctx, "antigravity-convergence", map[string]interface{}{
		"upstream_type":              UpstreamAntigravity,
		"access_token":               "access",
		"refresh_token":              "refresh",
		"project_id":                 "project-a",
		"antigravity_quota":          `{"models":[],"updated_at":"2026-08-27T00:00:00Z"}`,
		"antigravity_permissions":    `{"allowed":true,"updated_at":"2026-08-27T00:00:00Z"}`,
		"antigravity_entitlements":   `{"allowed":true,"updated_at":"2026-08-27T00:00:00Z"}`,
		"antigravity_sync_error":     "",
		"antigravity_sync_warning":   "",
		"antigravity_last_synced_at": "2026-08-27T00:00:00Z",
	}, "")
	if err != nil {
		t.Fatalf("InsertAccountWithCredentials: %v", err)
	}
	waitForSchedulerProjection(t, func() bool { return store.FindByID(accountID) != nil })
	account := store.FindByID(accountID)
	account.mu.Lock()
	account.SuccessStreak = 6
	account.mu.Unlock()
	atomic.StoreInt64(&account.ActiveRequests, 2)

	reserve5h := database.OptionalNullInt64{Set: true, Value: sql.NullInt64{Int64: 12, Valid: true}}
	reserve7d := database.OptionalNullInt64{Set: true, Value: sql.NullInt64{Int64: 24, Valid: true}}
	if err := db.UpdateAccountSchedulerMetadata(ctx, accountID, database.OptionalNullInt64{}, database.OptionalNullInt64{}, reserve5h, reserve7d, database.OptionalBool{}, database.OptionalInt64Slice{}, database.OptionalStringSlice{}, database.OptionalInt64Slice{}, database.OptionalString{}, nil); err != nil {
		t.Fatalf("set reserves: %v", err)
	}
	waitForSchedulerProjection(t, func() bool {
		account.mu.RLock()
		defer account.mu.RUnlock()
		return account.UsageReservePercent5h != nil && *account.UsageReservePercent5h == 12 &&
			account.UsageReservePercent7d != nil && *account.UsageReservePercent7d == 24
	})
	account.mu.RLock()
	successStreak := account.SuccessStreak
	account.mu.RUnlock()
	if atomic.LoadInt64(&account.ActiveRequests) != 2 || successStreak != 6 {
		t.Fatalf("reserve projection clobbered runtime state: active=%d success=%d", atomic.LoadInt64(&account.ActiveRequests), successStreak)
	}

	clearReserve := database.OptionalNullInt64{Set: true, Value: sql.NullInt64{}}
	if err := db.UpdateAccountSchedulerMetadata(ctx, accountID, database.OptionalNullInt64{}, database.OptionalNullInt64{}, clearReserve, clearReserve, database.OptionalBool{}, database.OptionalInt64Slice{}, database.OptionalStringSlice{}, database.OptionalInt64Slice{}, database.OptionalString{}, nil); err != nil {
		t.Fatalf("clear reserves: %v", err)
	}
	waitForSchedulerProjection(t, func() bool {
		account.mu.RLock()
		defer account.mu.RUnlock()
		return account.UsageReservePercent5h == nil && account.UsageReservePercent7d == nil
	})

	assertFence := func(label string, wantBlocked bool) {
		t.Helper()
		waitForSchedulerProjection(t, func() bool {
			account.mu.RLock()
			blocked := account.AntigravityHardBlocked
			account.mu.RUnlock()
			return blocked == wantBlocked && account.IsAvailable() != wantBlocked
		})
	}
	if err := db.UpdateCredentials(ctx, accountID, map[string]interface{}{
		"antigravity_quota": `{"models":[],"forbidden":true,"updated_at":"2026-08-27T00:01:00Z"}`,
	}); err != nil {
		t.Fatalf("deny quota: %v", err)
	}
	assertFence("quota denial", true)
	if err := db.UpdateCredentials(ctx, accountID, map[string]interface{}{
		"antigravity_quota": `{"models":[],"updated_at":"2026-08-27T00:02:00Z"}`,
	}); err != nil {
		t.Fatalf("recover quota: %v", err)
	}
	assertFence("quota recovery", false)

	if err := db.UpdateCredentials(ctx, accountID, map[string]interface{}{
		"antigravity_permissions": `{"allowed":false,"reason":"policy denied","updated_at":"2026-08-27T00:03:00Z"}`,
	}); err != nil {
		t.Fatalf("deny permission: %v", err)
	}
	assertFence("permission denial", true)
	if err := db.UpdateCredentials(ctx, accountID, map[string]interface{}{
		"antigravity_permissions": `{"allowed":true,"updated_at":"2026-08-27T00:04:00Z"}`,
	}); err != nil {
		t.Fatalf("recover permission: %v", err)
	}
	assertFence("permission recovery", false)

	const permanent = "Antigravity token refresh failed: invalid_grant"
	if err := db.UpdateCredentials(ctx, accountID, map[string]interface{}{
		"antigravity_sync_error":              permanent,
		"antigravity_permanent_refresh_error": permanent,
	}); err != nil {
		t.Fatalf("set permanent refresh fence: %v", err)
	}
	assertFence("permanent refresh denial", true)
	if err := db.UpdateCredentials(ctx, accountID, map[string]interface{}{
		"antigravity_sync_error":              "",
		"antigravity_permanent_refresh_error": "",
	}); err != nil {
		t.Fatalf("clear permanent refresh fence: %v", err)
	}
	assertFence("permanent refresh recovery", false)

	if atomic.LoadInt64(&account.ActiveRequests) != 2 {
		t.Fatalf("Antigravity projections clobbered active requests: %d", atomic.LoadInt64(&account.ActiveRequests))
	}
}

func TestIndexedAvailabilityWaitWakesOnOutboxAccountInsert(t *testing.T) {
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "scheduler-wait.db"))
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	store := NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 1, SchedulerEngine: "indexed"})
	t.Cleanup(func() {
		store.Stop()
		_ = db.Close()
	})
	if err := store.Init(ctx); err != nil {
		t.Fatalf("Store.Init: %v", err)
	}

	result := make(chan *Account, 1)
	go func() {
		acc, _ := store.WaitForSessionAvailableWithDispatch(ctx, "", 10*time.Second, 0, nil, nil, DispatchPolicyStandard)
		result <- acc
	}()
	waitForSchedulerProjection(t, func() bool { return store.GetSchedulerMetrics().Waiters == 1 })

	accountID, err := db.InsertOpenAIResponsesAccount(ctx, "wake-account", map[string]interface{}{
		"upstream_type": UpstreamOpenAIResponses,
		"base_url":      "https://wake.example",
		"api_key":       "sk-wake",
	}, "")
	if err != nil {
		t.Fatalf("InsertOpenAIResponsesAccount: %v", err)
	}
	select {
	case acc := <-result:
		if acc == nil || acc.ID() != accountID {
			t.Fatalf("wait result = %v, want account %d", acc, accountID)
		}
		store.Release(acc)
	case <-time.After(11 * time.Second):
		t.Fatal("availability wait was not woken by outbox account insert")
	}
}

func TestIsSchedulerOutboxTerminalError(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name     string
		ctx      context.Context
		err      error
		terminal bool
	}{
		{"nil error", context.Background(), nil, false},
		{"consumer ctx canceled", canceled, context.Canceled, true},
		// 子调用自带超时(如 ReloadProxyPool 5s)不能杀死消费者。
		{"sub-call deadline", context.Background(), context.DeadlineExceeded, false},
		{"conn done is transient", context.Background(), sql.ErrConnDone, false},
		{"database closed", context.Background(), errWrap("sql: database is closed"), true},
		{"plain network error", context.Background(), errWrap("read tcp: connection reset"), false},
	}
	for _, tc := range cases {
		if got := isSchedulerOutboxTerminalError(tc.ctx, tc.err); got != tc.terminal {
			t.Errorf("%s: terminal = %v, want %v", tc.name, got, tc.terminal)
		}
	}
}

func errWrap(message string) error {
	return fmt.Errorf("%s", message)
}

func TestSchedulerOutboxCursorHoleTracking(t *testing.T) {
	now := time.Now()
	cursor := &schedulerOutboxCursor{watermark: 10, holes: make(map[int64]time.Time)}
	events := []database.SchedulerOutboxEvent{{ID: 11}, {ID: 14}, {ID: 15}}
	cursor.noteHoles(events, now)
	if len(cursor.holes) != 2 {
		t.Fatalf("holes = %v, want ids 12 and 13", cursor.holes)
	}
	for _, id := range []int64{12, 13} {
		if _, ok := cursor.holes[id]; !ok {
			t.Fatalf("hole %d not tracked: %v", id, cursor.holes)
		}
	}

	due := cursor.dueHoleIDs(now)
	if len(due) != 2 {
		t.Fatalf("due holes = %v, want 2", due)
	}
	// 超过宽限期的空洞视为已回滚的事务,自动放弃。
	expired := cursor.dueHoleIDs(now.Add(schedulerOutboxHoleGrace + time.Minute))
	if len(expired) != 0 || len(cursor.holes) != 0 {
		t.Fatalf("expired holes should be dropped, got due=%v holes=%v", expired, cursor.holes)
	}
}

func TestApplyPersistentAccountSnapshotRoutingInvalidationGate(t *testing.T) {
	accounts := sparseRoutingAccounts(16, 9)
	store := newIndexedRoutingTestStore(accounts)
	store.SetAPIKeyAllowedGroups(21, []int64{9})
	if acc := store.NextExcluding(21, nil); acc != nil {
		store.Release(acc)
	}
	baseline := store.GetSchedulerMetrics().RoutingCacheInvalidations

	dst := accounts[len(accounts)-1]
	statusOnly := newFastSchedulerTestAccount(dst.DBID, HealthTierHealthy, 100, 1)
	statusOnly.GroupIDs = cloneInt64Slice(dst.GroupIDs)
	statusOnly.Status = StatusCooldown
	store.applyPersistentAccountSnapshot(dst, statusOnly, true)
	if got := store.GetSchedulerMetrics().RoutingCacheInvalidations; got != baseline {
		t.Fatalf("status-only snapshot invalidated routing cache: %d -> %d", baseline, got)
	}

	moved := newFastSchedulerTestAccount(dst.DBID, HealthTierHealthy, 100, 1)
	moved.GroupIDs = []int64{1}
	store.applyPersistentAccountSnapshot(dst, moved, true)
	if got := store.GetSchedulerMetrics().RoutingCacheInvalidations; got <= baseline {
		t.Fatalf("membership snapshot did not invalidate routing cache (still %d)", got)
	}
}

func TestApplyPersistentAccountSnapshotPreservesRuntimeState(t *testing.T) {
	store := newIndexedRoutingTestStore(nil)
	dst := newFastSchedulerTestAccount(1, HealthTierWarm, 100, 1)
	dst.usageObservedAt = time.Now()
	atomic.StoreInt64(&dst.ActiveRequests, 3)
	dst.SuccessStreak = 5
	dst.FailureStreak = 4
	src := newFastSchedulerTestAccount(1, HealthTierHealthy, 100, 1)
	src.CredentialGeneration = dst.CredentialGeneration

	store.applyPersistentAccountSnapshot(dst, src, true)
	if atomic.LoadInt64(&dst.ActiveRequests) != 3 || dst.SuccessStreak != 5 || dst.FailureStreak != 4 {
		t.Fatalf("runtime state clobbered: active=%d success=%d failure=%d", atomic.LoadInt64(&dst.ActiveRequests), dst.SuccessStreak, dst.FailureStreak)
	}
	if dst.usageObservedAt.IsZero() {
		t.Fatal("persistent snapshot should not erase a newer runtime observation timestamp")
	}

	rotated := newFastSchedulerTestAccount(1, HealthTierHealthy, 100, 1)
	rotated.CredentialGeneration = dst.CredentialGeneration + 1
	rotated.SuccessStreak = 9
	store.applyPersistentAccountSnapshot(dst, rotated, true)
	if dst.SuccessStreak != 0 {
		t.Fatalf("identity change should reset streaks, got %d", dst.SuccessStreak)
	}
}

func TestApplyPersistentAccountSnapshotCopiesAntigravityStateAndClearsFence(t *testing.T) {
	store := newIndexedRoutingTestStore(nil)
	dst := newFastSchedulerTestAccount(1, HealthTierWarm, 100, 1)
	dst.UpstreamType = UpstreamAntigravity
	dst.AntigravityProjectID = "stale-project"
	dst.AntigravityHardBlocked = true
	dst.AntigravityHardBlockReason = "stale fence"
	dst.PlanType = "old-plan"
	dst.GrokClientID = "old-grok-client"
	dst.GrokLivePlan = "old-grok-plan"
	dst.UsageReservePercent5h = int64Ptr(5)
	dst.UsageReservePercent7d = int64Ptr(7)
	atomic.StoreInt64(&dst.ActiveRequests, 3)
	dst.SuccessStreak = 5

	src := newFastSchedulerTestAccount(1, HealthTierHealthy, 120, 2)
	src.UpstreamType = UpstreamAntigravity
	src.AccessToken = "fresh-access"
	src.AntigravityProjectID = "fresh-project"
	src.AntigravityHardBlocked = true
	src.AntigravityHardBlockReason = "Google quota API denied access"
	src.Status = StatusError
	src.PlanType = "new-plan"
	src.GrokClientID = "new-grok-client"
	src.GrokLivePlan = "new-grok-plan"
	src.UsageReservePercent5h = int64Ptr(15)
	src.UsageReservePercent7d = int64Ptr(27)
	src.CredentialGeneration = dst.CredentialGeneration

	store.applyPersistentAccountSnapshot(dst, src, true)
	dst.mu.RLock()
	project := dst.AntigravityProjectID
	hardBlocked := dst.AntigravityHardBlocked
	hardReason := dst.AntigravityHardBlockReason
	plan := dst.PlanType
	grokClient := dst.GrokClientID
	grokPlan := dst.GrokLivePlan
	reserve5h := dst.UsageReservePercent5h
	reserve7d := dst.UsageReservePercent7d
	dst.mu.RUnlock()
	if project != "fresh-project" || !hardBlocked || hardReason != "Google quota API denied access" {
		t.Fatalf("Antigravity snapshot = project:%q blocked:%v reason:%q, want fresh durable state", project, hardBlocked, hardReason)
	}
	if plan != "new-plan" || grokClient != "new-grok-client" || grokPlan != "new-grok-plan" {
		t.Fatalf("generic/Grok snapshot regressed: plan=%q grokClient=%q grokPlan=%q", plan, grokClient, grokPlan)
	}
	if reserve5h == nil || *reserve5h != 15 || reserve7d == nil || *reserve7d != 27 {
		t.Fatalf("reserve snapshot = %v/%v, want 15/27", reserve5h, reserve7d)
	}
	*src.UsageReservePercent5h = 99
	if *reserve5h != 15 {
		t.Fatal("reserve snapshot retained source pointer")
	}
	if dst.IsAvailable() {
		t.Fatal("hard-blocked Antigravity account remained available after snapshot update")
	}
	if atomic.LoadInt64(&dst.ActiveRequests) != 3 || dst.SuccessStreak != 5 {
		t.Fatalf("snapshot clobbered runtime state: active=%d streak=%d", atomic.LoadInt64(&dst.ActiveRequests), dst.SuccessStreak)
	}

	// A successful provider sync clears the durable fence while retaining the
	// newly selected project and the other persisted projections.
	src.AntigravityHardBlocked = false
	src.AntigravityHardBlockReason = ""
	src.Status = StatusReady
	store.applyPersistentAccountSnapshot(dst, src, true)
	dst.mu.RLock()
	hardBlocked = dst.AntigravityHardBlocked
	hardReason = dst.AntigravityHardBlockReason
	project = dst.AntigravityProjectID
	dst.mu.RUnlock()
	if hardBlocked || hardReason != "" || project != "fresh-project" {
		t.Fatalf("cleared Antigravity fence = project:%q blocked:%v reason:%q, want project retained and fence cleared", project, hardBlocked, hardReason)
	}
	if !dst.IsAvailable() {
		t.Fatal("cleared Antigravity fence did not restore availability")
	}

	src.UsageReservePercent5h = nil
	src.UsageReservePercent7d = nil
	store.applyPersistentAccountSnapshot(dst, src, true)
	dst.mu.RLock()
	reserve5h = dst.UsageReservePercent5h
	reserve7d = dst.UsageReservePercent7d
	dst.mu.RUnlock()
	if reserve5h != nil || reserve7d != nil {
		t.Fatalf("cleared reserve snapshot = %v/%v, want nil/nil", reserve5h, reserve7d)
	}

	// Identity sync may also clear project_id; the runtime copy must not retain
	// the previous instance's project and must consequently stop dispatch.
	src.AntigravityProjectID = ""
	store.applyPersistentAccountSnapshot(dst, src, true)
	dst.mu.RLock()
	project = dst.AntigravityProjectID
	dst.mu.RUnlock()
	if project != "" {
		t.Fatalf("cleared Antigravity project = %q, want empty", project)
	}
	if dst.IsAvailable() {
		t.Fatal("Antigravity account without project remained available after snapshot clear")
	}
}

func TestReloadDispatchAccountsByIDsAppliesBatchProjection(t *testing.T) {
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "scheduler-batch-reload.db"))
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	store := NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 1, SchedulerEngine: "indexed"})
	t.Cleanup(func() {
		store.Stop()
		_ = db.Close()
	})

	groupID, err := db.CreateAccountGroup(ctx, "outbox-batch", "", "", 0, 0, sql.NullInt64{})
	if err != nil {
		t.Fatalf("CreateAccountGroup: %v", err)
	}
	firstID, err := db.InsertOpenAIResponsesAccount(ctx, "batch-first", map[string]interface{}{
		"upstream_type": UpstreamOpenAIResponses,
		"base_url":      "https://batch-first.example",
		"api_key":       "sk-batch-first",
	}, "")
	if err != nil {
		t.Fatalf("InsertOpenAIResponsesAccount(first): %v", err)
	}
	secondID, err := db.InsertOpenAIResponsesAccount(ctx, "batch-second", map[string]interface{}{
		"upstream_type": UpstreamOpenAIResponses,
		"base_url":      "https://batch-second.example",
		"api_key":       "sk-batch-second",
	}, "")
	if err != nil {
		t.Fatalf("InsertOpenAIResponsesAccount(second): %v", err)
	}
	if err := db.SetAccountGroups(ctx, firstID, []int64{groupID}); err != nil {
		t.Fatalf("SetAccountGroups: %v", err)
	}
	if err := db.SetModelCooldown(ctx, firstID, "gpt-5.6", "rate_limited", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("SetModelCooldown: %v", err)
	}

	if err := store.reloadDispatchAccountsByIDs(ctx, []int64{secondID, firstID, firstID, 0}); err != nil {
		t.Fatalf("reloadDispatchAccountsByIDs(initial): %v", err)
	}
	first := store.FindByID(firstID)
	if first == nil || store.FindByID(secondID) == nil {
		t.Fatalf("batch reload did not add both accounts: first=%v second=%v", first, store.FindByID(secondID))
	}
	first.mu.Lock()
	groups := cloneInt64Slice(first.GroupIDs)
	_, hasCooldown := first.ModelCooldowns["gpt-5.6"]
	first.mu.Unlock()
	if len(groups) != 1 || groups[0] != groupID || !hasCooldown {
		t.Fatalf("first projection groups=%v cooldown=%v, want group %d and active cooldown", groups, hasCooldown, groupID)
	}

	if err := db.SetAccountEnabled(ctx, firstID, false); err != nil {
		t.Fatalf("SetAccountEnabled: %v", err)
	}
	if err := db.SoftDeleteAccount(ctx, secondID); err != nil {
		t.Fatalf("SoftDeleteAccount: %v", err)
	}
	if err := store.reloadDispatchAccountsByIDs(ctx, []int64{firstID, secondID}); err != nil {
		t.Fatalf("reloadDispatchAccountsByIDs(update/delete): %v", err)
	}
	if atomic.LoadInt32(&first.DispatchPaused) == 0 {
		t.Fatal("disabled account remained dispatchable after batch reload")
	}
	if store.FindByID(secondID) != nil {
		t.Fatal("deleted account remained in the runtime pool after batch reload")
	}
}
