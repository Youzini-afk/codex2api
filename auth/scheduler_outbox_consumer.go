package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/codex2api/database"
)

const (
	schedulerOutboxPollInterval = 250 * time.Millisecond
	schedulerOutboxBatchSize    = 500
	schedulerOutboxMaxBatches   = 4
	schedulerOutboxRetention    = 7 * 24 * time.Hour
	// schedulerOutboxHoleGrace both defines the startup replay window and bounds
	// how long a watermark hole is re-polled.
	// BIGSERIAL ids are assigned at INSERT but become visible at COMMIT, so an
	// id can surface after the watermark passed it. The grace must cover the
	// longest realistic writing transaction (bulk imports run minutes); holes
	// left by rolled-back inserts simply expire. A transaction open longer than
	// this remains outside the event-stream guarantee and falls back to the
	// five-minute reconciliation path below.
	schedulerOutboxHoleGrace = 10 * time.Minute
	schedulerOutboxMaxHoles  = 10000
	// schedulerOutboxReconcileEvery is the belt-and-braces full reconcile: it
	// repairs anything the event stream missed (skipped hole past its grace, a
	// consumer bug) without putting scans back on the request path.
	schedulerOutboxReconcileEvery = 5 * time.Minute
	schedulerOutboxCleanupLoops   = 20
)

// SystemSettingsApplyHook lets the process root publish the parts of one
// committed settings snapshot owned by other packages without introducing an
// auth -> proxy dependency. The hook must be installed before Store.Init.
type SystemSettingsApplyHook func(context.Context, *database.SystemSettings) error

// SetSystemSettingsApplyHook installs the process-level runtime projection.
// It shares the settings apply mutex so replacing a hook can never interleave
// with an in-flight snapshot application.
func (s *Store) SetSystemSettingsApplyHook(hook SystemSettingsApplyHook) {
	if s == nil {
		return
	}
	s.systemSettingsApplyMu.Lock()
	s.systemSettingsApplyHook = hook
	s.systemSettingsApplyMu.Unlock()
}

type schedulerOutboxKey struct {
	entityType string
	entityID   int64
}

// schedulerOutboxCursor is owned by the consumer goroutine; no locking.
type schedulerOutboxCursor struct {
	watermark int64
	holes     map[int64]time.Time // id -> re-poll deadline
}

// noteSchedulerOutboxHoles records ids in (watermark, lastID] absent from the
// batch so late-committing transactions are not skipped forever.
func (c *schedulerOutboxCursor) noteHoles(events []database.SchedulerOutboxEvent, now time.Time) {
	prev := c.watermark
	deadline := now.Add(schedulerOutboxHoleGrace)
	for _, event := range events {
		for id := prev + 1; id < event.ID; id++ {
			if len(c.holes) >= schedulerOutboxMaxHoles {
				return
			}
			c.holes[id] = deadline
		}
		prev = event.ID
	}
}

func (c *schedulerOutboxCursor) dueHoleIDs(now time.Time) []int64 {
	if len(c.holes) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(c.holes))
	for id, deadline := range c.holes {
		if now.After(deadline) {
			delete(c.holes, id)
			continue
		}
		ids = append(ids, id)
		if len(ids) >= schedulerOutboxBatchSize {
			break
		}
	}
	return ids
}

// startSchedulerOutboxConsumer starts after the initial account snapshot has
// loaded. startWatermark is the newest event older than the startup grace
// window, so changes committed during snapshot loading and all recent visible
// events are idempotently replayed.
func (s *Store) startSchedulerOutboxConsumer(startWatermark int64) {
	if s == nil || s.db == nil || !s.schedulerOutboxStarted.CompareAndSwap(false, true) {
		return
	}
	if s.schedulerMetrics != nil {
		s.schedulerMetrics.outboxWatermark.Store(startWatermark)
		s.schedulerMetrics.outboxHighWatermark.Store(startWatermark)
	}
	ctx := s.backgroundCtx
	if ctx == nil {
		ctx = context.Background()
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.schedulerOutboxStarted.Store(false)
		poll := time.NewTicker(schedulerOutboxPollInterval)
		cleanup := time.NewTicker(time.Hour)
		reconcile := time.NewTicker(schedulerOutboxReconcileEvery)
		defer poll.Stop()
		defer cleanup.Stop()
		defer reconcile.Stop()

		cursor := &schedulerOutboxCursor{watermark: startWatermark, holes: make(map[int64]time.Time)}
		errStreak := 0
		var retryNotBefore time.Time
		handle := func(err error) bool {
			if err == nil {
				errStreak = 0
				return true
			}
			if isSchedulerOutboxTerminalError(ctx, err) {
				return false
			}
			// 瞬时错误退避重试:错误连击越多,跳过的 poll tick 越多,封顶 5s。
			errStreak++
			backoff := time.Duration(min(errStreak, 20)) * schedulerOutboxPollInterval
			retryNotBefore = time.Now().Add(backoff)
			return true
		}

		// Do not wait one poll interval for mutations committed during startup.
		if !handle(s.consumeSchedulerOutbox(ctx, cursor)) {
			return
		}
		for {
			select {
			case <-poll.C:
				if time.Now().Before(retryNotBefore) {
					continue
				}
				if !handle(s.consumeSchedulerOutbox(ctx, cursor)) {
					return
				}
			case <-reconcile.C:
				// 事件流之外的兜底:低频全量对账修复任何被漏掉的状态。
				if err := s.reloadSystemSettings(ctx); err != nil {
					s.recordSchedulerOutboxError(fmt.Errorf("reconcile settings snapshot: %w", err))
					if !handle(err) {
						return
					}
					continue
				}
				handle(nil)
				s.TriggerDispatchStateReconcileAsync()
			case <-cleanup.C:
				cutoff := time.Now().Add(-schedulerOutboxRetention)
				for i := 0; i < schedulerOutboxCleanupLoops; i++ {
					deleted, err := s.db.CleanupSchedulerOutboxThrough(ctx, cutoff, cursor.watermark, 10000)
					if err != nil {
						if isSchedulerOutboxTerminalError(ctx, err) {
							return
						}
						if !errors.Is(err, context.Canceled) {
							log.Printf("清理调度 outbox 失败: %v", err)
						}
						break
					}
					if deleted < 10000 {
						break
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (s *Store) consumeSchedulerOutbox(ctx context.Context, cursor *schedulerOutboxCursor) error {
	if s == nil || s.db == nil || ctx.Err() != nil {
		return ctx.Err()
	}

	// Re-poll watermark holes first: late-committed events carry ids the
	// watermark already passed, so the regular id>watermark query misses them.
	if holeIDs := cursor.dueHoleIDs(time.Now()); len(holeIDs) > 0 {
		events, err := s.db.ListSchedulerOutboxEventsByIDs(ctx, holeIDs)
		if err != nil {
			s.recordSchedulerOutboxError(err)
			return err
		}
		if len(events) > 0 {
			if err := s.applySchedulerOutboxBatch(ctx, events); err != nil {
				return err
			}
			for _, event := range events {
				delete(cursor.holes, event.ID)
			}
			if s.schedulerMetrics != nil {
				s.schedulerMetrics.outboxEvents.Add(uint64(len(events)))
			}
		}
	}

	for batch := 0; batch < schedulerOutboxMaxBatches; batch++ {
		events, err := s.db.ListSchedulerOutboxEventsAfter(ctx, cursor.watermark, schedulerOutboxBatchSize)
		if err != nil {
			s.recordSchedulerOutboxError(err)
			return err
		}
		if len(events) == 0 {
			if s.schedulerMetrics != nil {
				s.schedulerMetrics.outboxHighWatermark.Store(cursor.watermark)
				s.schedulerMetrics.outboxLagNS.Store(0)
			}
			return nil
		}

		cursor.noteHoles(events, time.Now())
		if err := s.applySchedulerOutboxBatch(ctx, events); err != nil {
			return err
		}

		cursor.watermark = events[len(events)-1].ID
		if s.schedulerMetrics != nil {
			metrics := s.schedulerMetrics
			metrics.outboxWatermark.Store(cursor.watermark)
			metrics.outboxHighWatermark.Store(cursor.watermark)
			metrics.outboxEvents.Add(uint64(len(events)))
			metrics.outboxBatches.Add(1)
			metrics.outboxLastAppliedNS.Store(time.Now().UnixNano())
			if createdAt := events[len(events)-1].CreatedAt; !createdAt.IsZero() {
				lag := time.Since(createdAt)
				if lag < 0 {
					lag = 0
				}
				metrics.outboxLagNS.Store(int64(lag))
			}
		}
		if len(events) < schedulerOutboxBatchSize {
			return nil
		}
	}

	// A full drain was deliberately bounded. Expose the actual high watermark
	// so operators can see backlog while later ticks continue processing it.
	if high, err := s.db.SchedulerOutboxHighWatermark(ctx); err == nil && s.schedulerMetrics != nil {
		s.schedulerMetrics.outboxHighWatermark.Store(high)
	} else if err != nil {
		return err
	}
	return nil
}

func (s *Store) applySchedulerOutboxBatch(ctx context.Context, events []database.SchedulerOutboxEvent) error {
	// Collapse repeated changes to the same projection row within a batch.
	// Sorting by the last event id preserves dependency order between the
	// remaining distinct entities.
	latest := make(map[schedulerOutboxKey]database.SchedulerOutboxEvent, len(events))
	for _, event := range events {
		event.EntityType = strings.ToLower(strings.TrimSpace(event.EntityType))
		entityID := event.EntityID
		// Proxy and settings projections are reloaded as a whole. Multiple
		// row events in one poll therefore collapse to one reload instead of
		// repeating identical database work for every changed row.
		if event.EntityType == database.SchedulerEntityProxy || event.EntityType == database.SchedulerEntitySettings {
			entityID = 0
		}
		key := schedulerOutboxKey{entityType: event.EntityType, entityID: entityID}
		latest[key] = event
	}
	coalesced := make([]database.SchedulerOutboxEvent, 0, len(latest))
	for _, event := range latest {
		coalesced = append(coalesced, event)
	}
	sort.Slice(coalesced, func(i, j int) bool { return coalesced[i].ID < coalesced[j].ID })

	accountIDs := make([]int64, 0, len(coalesced))
	for _, event := range coalesced {
		if event.EntityType == database.SchedulerEntityAccount {
			accountIDs = append(accountIDs, event.EntityID)
			continue
		}
		if err := s.applySchedulerOutboxEvent(ctx, event); err != nil {
			wrapped := fmt.Errorf("event %d %s/%d: %w", event.ID, event.EntityType, event.EntityID, err)
			s.recordSchedulerOutboxError(wrapped)
			return wrapped
		}
	}
	// Account events are projections of final database state and can be
	// fetched together after group/settings changes have been applied. This
	// turns up to 500 * (account + cooldown + membership) base queries into
	// three bounded queries, which is essential during large cross-instance
	// imports. Provider-specific rich state is loaded only for providers that
	// need it.
	if err := s.reloadDispatchAccountsByIDs(ctx, accountIDs); err != nil {
		wrapped := fmt.Errorf("account projection batch through event %d: %w", events[len(events)-1].ID, err)
		s.recordSchedulerOutboxError(wrapped)
		return wrapped
	}
	return nil
}

// isSchedulerOutboxTerminalError intentionally treats almost nothing as
// terminal: the consumer is the only cross-instance propagation path for
// authorization-relevant state, so a transient timeout (e.g. ReloadProxyPool's
// internal 5s budget returning DeadlineExceeded) must never kill it. Terminal
// means our own context ended or the database handle is gone for good.
func isSchedulerOutboxTerminalError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is closed") || strings.Contains(message, "closed database")
}

func (s *Store) recordSchedulerOutboxError(err error) {
	if s != nil && s.schedulerMetrics != nil {
		s.schedulerMetrics.outboxErrors.Add(1)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("同步调度 outbox 失败: %v", err)
	}
}

func (s *Store) applySchedulerOutboxEvent(ctx context.Context, event database.SchedulerOutboxEvent) error {
	switch strings.ToLower(strings.TrimSpace(event.EntityType)) {
	case database.SchedulerEntityAccount:
		return s.reloadDispatchAccountByID(ctx, event.EntityID)
	case database.SchedulerEntityAPIKey:
		return s.reloadAPIKeyRoutingByID(ctx, event.EntityID)
	case database.SchedulerEntityGroup:
		return s.reloadAccountGroupRoutingByID(ctx, event.EntityID)
	case database.SchedulerEntityProxy:
		return s.ReloadProxyPool()
	case database.SchedulerEntitySettings:
		return s.reloadSystemSettings(ctx)
	default:
		return nil
	}
}

func (s *Store) reloadDispatchAccountByID(ctx context.Context, accountID int64) error {
	return s.reloadDispatchAccountsByIDs(ctx, []int64{accountID})
}

func (s *Store) reloadDispatchAccountsByIDs(ctx context.Context, accountIDs []int64) error {
	if s == nil || s.db == nil || len(accountIDs) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(accountIDs))
	ids := make([]int64, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		if accountID <= 0 {
			continue
		}
		if _, duplicate := seen[accountID]; duplicate {
			continue
		}
		seen[accountID] = struct{}{}
		ids = append(ids, accountID)
	}
	if len(ids) == 0 {
		return nil
	}

	rows, err := s.db.ListActiveByIDs(ctx, ids)
	if err != nil {
		return err
	}
	cooldownRows, err := s.db.ListActiveModelCooldownsForAccounts(ctx, ids)
	if err != nil {
		return err
	}
	memberships, err := s.db.ListAccountGroupMembershipsByAccountIDs(ctx, ids)
	if err != nil {
		return err
	}
	cooldownsByAccount := make(map[int64][]*database.AccountModelCooldownRow)
	for _, cooldown := range cooldownRows {
		cooldownsByAccount[cooldown.AccountID] = append(cooldownsByAccount[cooldown.AccountID], cooldown)
	}

	loaded := make(map[int64]struct{}, len(rows))
	removed := make([]int64, 0)
	added := make([]*Account, 0)
	for _, row := range rows {
		loaded[row.ID] = struct{}{}
		fresh := s.buildAccountFromRow(ctx, row, cooldownsByAccount)
		if fresh == nil {
			// A credential-less row must not remain dispatchable merely because
			// an older process still has its previous credentials in memory.
			if s.FindByID(row.ID) != nil {
				removed = append(removed, row.ID)
			}
			continue
		}
		fresh.mu.Lock()
		fresh.GroupIDs = cloneInt64Slice(memberships[row.ID])
		fresh.recomputeEffectiveAutoPause(s)
		fresh.recomputeEffectiveGroupBaseConcurrency(s)
		fresh.mu.Unlock()

		current := s.FindByID(row.ID)
		if current == nil {
			added = append(added, fresh)
			continue
		}
		s.applyPersistentAccountSnapshot(current, fresh, row.Enabled)
	}
	for _, accountID := range ids {
		if _, ok := loaded[accountID]; !ok && s.FindByID(accountID) != nil {
			removed = append(removed, accountID)
		}
	}
	if len(removed) > 0 {
		s.RemoveAccounts(removed)
	}
	if len(added) > 0 {
		s.AddAccounts(added)
	}
	return nil
}

func cloneBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneModelCooldownMap(values map[string]ModelCooldown) map[string]ModelCooldown {
	if len(values) == 0 {
		return nil
	}
	copy := make(map[string]ModelCooldown, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

// applyPersistentAccountSnapshot updates only database-backed fields. Live
// request counters, EWMA/failure history, reservations, and mutexes remain on
// the existing object, so an outbox update cannot invalidate an in-flight
// request's Release call or reset runtime pressure.
func (s *Store) applyPersistentAccountSnapshot(dst, src *Account, enabled bool) {
	if s == nil || dst == nil || src == nil {
		return
	}

	dst.mu.Lock()
	identityChanged := dst.CredentialGeneration != src.CredentialGeneration
	// Routing sub-pools only need invalidation when membership-relevant fields
	// move; status/cooldown/usage churn stays live through the shared Account
	// pointers (retainUnavailable). Without this gate, every 429-driven
	// cooldown event would wipe the whole per-key cache each poll tick.
	routingChanged := dst.PlanType != src.PlanType ||
		dst.UpstreamType != src.UpstreamType ||
		dst.GrokLivePlan != src.GrokLivePlan ||
		!slices.Equal(dst.GroupIDs, src.GroupIDs) ||
		!slices.Equal(dst.AllowedAPIKeyIDs, src.AllowedAPIKeyIDs)
	dst.RefreshToken = src.RefreshToken
	dst.UpstreamRequestIDHeader = src.UpstreamRequestIDHeader
	dst.SessionToken = src.SessionToken
	dst.AccessToken = src.AccessToken
	dst.ExpiresAt = src.ExpiresAt
	dst.AccountID = src.AccountID
	dst.Email = src.Email
	dst.PlanType = src.PlanType
	dst.AntigravityProjectID = src.AntigravityProjectID
	dst.AntigravityHardBlocked = src.AntigravityHardBlocked
	dst.AntigravityHardBlockReason = src.AntigravityHardBlockReason
	dst.ProxyURL = src.ProxyURL
	dst.CustomHeaders = cloneStringMap(src.CustomHeaders)
	dst.UpstreamType = src.UpstreamType
	dst.BaseURL = src.BaseURL
	dst.APIKey = src.APIKey
	dst.Models = cloneStringSlice(src.Models)
	dst.ModelMapping = src.ModelMapping
	dst.CodexClientMetadataMode = src.CodexClientMetadataMode
	dst.CodexPassthroughMode = src.CodexPassthroughMode
	dst.CodexFingerprintMode = src.CodexFingerprintMode
	dst.Timezone = src.Timezone
	dst.ClaudeFingerprintMode = src.ClaudeFingerprintMode
	dst.claudeSessionWindow = src.claudeSessionWindow
	dst.CodexAuthMode = src.CodexAuthMode
	dst.AgentRuntimeID = src.AgentRuntimeID
	dst.AgentPrivateKey = src.AgentPrivateKey
	dst.AgentTaskID = src.AgentTaskID
	dst.GrokClientID = src.GrokClientID
	dst.GrokTokenEndpoint = src.GrokTokenEndpoint
	dst.GrokOIDCIssuer = src.GrokOIDCIssuer
	dst.GrokPrincipalType = src.GrokPrincipalType
	dst.GrokPrincipalID = src.GrokPrincipalID
	dst.CredentialGeneration = src.CredentialGeneration
	dst.CredentialFamilyID = src.CredentialFamilyID
	dst.GrokLivePlan = src.GrokLivePlan
	dst.GrokLivePlanObservedAt = src.GrokLivePlanObservedAt
	dst.GrokLivePlanExpiresAt = src.GrokLivePlanExpiresAt
	dst.GrokLivePlanKnown = src.GrokLivePlanKnown
	dst.GrokAccessAllowed = cloneBoolPtr(src.GrokAccessAllowed)
	dst.GrokAccessExpiresAt = src.GrokAccessExpiresAt
	dst.GrokBillingExhausted = src.GrokBillingExhausted
	dst.GrokBillingExpiresAt = src.GrokBillingExpiresAt
	dst.GrokFactsGeneration = src.GrokFactsGeneration
	dst.grokRouting = src.grokRouting
	dst.grokRuntimeSink = s
	// Grok rate-limit/free-quota observations are published to memory before
	// their minute-batched database write. Startup replay can therefore carry an
	// older (or empty) persisted snapshot after live traffic already observed a
	// newer value. Keep the newest observation unless credentials changed, in
	// which case no quota state may cross the identity boundary.
	if identityChanged {
		dst.grokRateLimit = nil
		dst.grokFreeQuota = nil
		dst.grokRateLimitDirty = false
	}
	if src.grokRateLimit != nil && (dst.grokRateLimit == nil || !src.grokRateLimit.UpdatedAt.Before(dst.grokRateLimit.UpdatedAt)) {
		copied := *src.grokRateLimit
		dst.grokRateLimit = &copied
	}
	if src.grokFreeQuota != nil && (dst.grokFreeQuota == nil || !src.grokFreeQuota.ExhaustedAt.Before(dst.grokFreeQuota.ExhaustedAt)) {
		copied := *src.grokFreeQuota
		dst.grokFreeQuota = &copied
	}
	dst.Status = src.Status
	dst.CooldownUtil = src.CooldownUtil
	dst.CooldownReason = src.CooldownReason
	dst.ErrorMsg = src.ErrorMsg
	dst.UsagePercent7d = src.UsagePercent7d
	dst.UsagePercent7dValid = src.UsagePercent7dValid
	dst.Reset7dAt = src.Reset7dAt
	dst.Window7dSeconds = src.Window7dSeconds
	dst.UsagePercent5h = src.UsagePercent5h
	dst.UsagePercent5hValid = src.UsagePercent5hValid
	dst.Reset5hAt = src.Reset5hAt
	dst.UsageUpdatedAt = src.UsageUpdatedAt
	dst.UsageUpdatedAt5h = src.UsageUpdatedAt5h
	if src.usageObservedAt.After(dst.usageObservedAt) {
		dst.usageObservedAt = src.usageObservedAt
	}
	dst.UsagePercentSpark = src.UsagePercentSpark
	dst.UsagePercentSparkValid = src.UsagePercentSparkValid
	dst.ResetSparkAt = src.ResetSparkAt
	dst.UsageUpdatedAtSpark = src.UsageUpdatedAtSpark
	dst.AutoPause5hThreshold = src.AutoPause5hThreshold
	dst.AutoPause7dThreshold = src.AutoPause7dThreshold
	dst.AutoPause5hDisabled = src.AutoPause5hDisabled
	dst.AutoPause7dDisabled = src.AutoPause7dDisabled
	dst.DispatchCountLimit = src.DispatchCountLimit
	dst.SchedulerPriority = src.SchedulerPriority
	dst.ScoreBiasOverride = cloneInt64Ptr(src.ScoreBiasOverride)
	dst.BaseConcurrencyOverride = cloneInt64Ptr(src.BaseConcurrencyOverride)
	dst.UsageReservePercent5h = cloneInt64Ptr(src.UsageReservePercent5h)
	dst.UsageReservePercent7d = cloneInt64Ptr(src.UsageReservePercent7d)
	dst.CreditEnabled = src.CreditEnabled
	dst.CreditSkipUsageWindow = src.CreditSkipUsageWindow
	dst.IgnoreUsageLimitStatusOverride = cloneBoolPtr(src.IgnoreUsageLimitStatusOverride)
	dst.SkipWarmTier = src.SkipWarmTier
	dst.AllowedAPIKeyIDs = cloneInt64Slice(src.AllowedAPIKeyIDs)
	dst.setAllowedAPIKeyIDsLocked(src.AllowedAPIKeyIDs)
	dst.Tags = cloneStringSlice(src.Tags)
	dst.GroupIDs = cloneInt64Slice(src.GroupIDs)
	dst.ModelCooldowns = cloneModelCooldownMap(src.ModelCooldowns)
	dst.ModelCooldownModeOverride = cloneStringPtr(src.ModelCooldownModeOverride)
	dst.ModelCooldownSecondsOverride = cloneIntPtr(src.ModelCooldownSecondsOverride)
	dst.ModelCooldownBackoffOverride = cloneBoolPtr(src.ModelCooldownBackoffOverride)
	dst.SubscriptionExpiresAt = src.SubscriptionExpiresAt
	if identityChanged {
		dst.HealthTier = src.HealthTier
		dst.SuccessStreak = 0
		dst.FailureStreak = 0
		dst.PermanentRefreshFailures = 0
		dst.LastFailureKind = ""
	}
	dst.recomputeEffectiveIgnoreUsageLimitStatus(s.IgnoreUsageLimitStatus())
	dst.recomputeEffectiveGroupBaseConcurrency(s)
	dst.recomputeEffectiveAutoPause(s)
	dst.recomputeSchedulerLocked(atomic.LoadInt64(&s.maxConcurrency))
	dst.mu.Unlock()

	if src.Locked != 0 {
		atomic.StoreInt32(&dst.Locked, 1)
	} else {
		atomic.StoreInt32(&dst.Locked, 0)
	}
	if enabled {
		atomic.StoreInt32(&dst.DispatchPaused, 0)
	} else {
		atomic.StoreInt32(&dst.DispatchPaused, 1)
	}
	if routingChanged {
		s.invalidateRoutingSchedulers()
	}
	s.fastSchedulerUpdate(dst)
	s.notifySchedulerAvailability()
}

func (s *Store) reloadAPIKeyRoutingByID(ctx context.Context, apiKeyID int64) error {
	if apiKeyID <= 0 {
		return nil
	}
	row, err := s.db.GetAPIKeyByID(ctx, apiKeyID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	s.apiKeyGroupsMu.Lock()
	delete(s.apiKeyAllowedGroups, apiKeyID)
	delete(s.apiKeyAllowedGroupSets, apiKeyID)
	delete(s.apiKeyNoAffinityGroups, apiKeyID)
	delete(s.apiKeyNoAffinityGroupSets, apiKeyID)
	delete(s.apiKeyAllowedPlans, apiKeyID)
	delete(s.apiKeyAllowedPlanSets, apiKeyID)
	delete(s.apiKeyUpstreamChannels, apiKeyID)
	if row != nil {
		groups := normalizeAllowedGroupIDs(row.AllowedGroupIDs)
		if len(groups) > 0 {
			s.apiKeyAllowedGroups[apiKeyID] = cloneInt64Slice(groups)
			s.apiKeyAllowedGroupSets[apiKeyID] = int64Set(groups)
		}
		noAffinity := normalizeAllowedGroupIDs(row.Limits.NoAffinityGroupIDs)
		if len(noAffinity) > 0 {
			s.apiKeyNoAffinityGroups[apiKeyID] = cloneInt64Slice(noAffinity)
			s.apiKeyNoAffinityGroupSets[apiKeyID] = int64Set(noAffinity)
		}
		plans := normalizeAllowedPlans(row.Limits.PlanAllow)
		if len(plans) > 0 {
			s.apiKeyAllowedPlans[apiKeyID] = append([]string(nil), plans...)
			s.apiKeyAllowedPlanSets[apiKeyID] = stringSet(plans)
		}
		if channel := row.Limits.ResolveUpstreamChannel(); channel != "" {
			s.apiKeyUpstreamChannels[apiKeyID] = channel
		}
	}
	s.apiKeyGroupsMu.Unlock()
	// 全局索引的内容与 api_key 行无关(组/plan/渠道限制在取号时经
	// groupCheck 实时校验),这里只需丢弃按 key 缓存的子池。
	s.invalidateRoutingSchedulers()
	s.notifySchedulerAvailability()
	return nil
}

func (s *Store) reloadAccountGroupRoutingByID(ctx context.Context, groupID int64) error {
	if groupID <= 0 {
		return nil
	}
	groups, err := s.db.ListAccountGroups(ctx)
	if err != nil {
		return err
	}
	var found *database.AccountGroup
	for i := range groups {
		if groups[i].ID == groupID {
			found = &groups[i]
			break
		}
	}
	if found == nil {
		s.groupAutoPauseThresholds.Delete(groupID)
		s.groupBaseConcurrencyOverrides.Delete(groupID)
		s.groupProxyURLs.Delete(groupID)
		s.groupNames.Delete(groupID)
		return nil
	}
	if found.AutoPause5hThreshold > 0 || found.AutoPause7dThreshold > 0 {
		s.groupAutoPauseThresholds.Store(groupID, [2]float64{found.AutoPause5hThreshold, found.AutoPause7dThreshold})
	} else {
		s.groupAutoPauseThresholds.Delete(groupID)
	}
	if found.BaseConcurrencyOverride.Valid {
		s.groupBaseConcurrencyOverrides.Store(groupID, found.BaseConcurrencyOverride.Int64)
	} else {
		s.groupBaseConcurrencyOverrides.Delete(groupID)
	}
	s.SetGroupProxyURLs(groupID, found.ProxyURLs)
	s.groupNames.Store(groupID, strings.TrimSpace(found.Name))

	accountIDs, err := s.db.ListAccountIDsInGroups(ctx, []int64{groupID})
	if err != nil {
		return err
	}
	for _, accountID := range accountIDs {
		if acc := s.FindByID(accountID); acc != nil {
			acc.mu.Lock()
			acc.recomputeEffectiveGroupBaseConcurrency(s)
			acc.recomputeEffectiveAutoPause(s)
			acc.recomputeSchedulerLocked(atomic.LoadInt64(&s.maxConcurrency))
			acc.mu.Unlock()
			s.fastSchedulerUpdate(acc)
		}
	}
	s.notifySchedulerAvailability()
	return nil
}

// reloadSystemSettings reads one committed database snapshot, publishes every
// Store-owned request-path setting through existing setters, then invokes the
// process-root hook for proxy/database globals. A failed hook returns an error
// to the outbox consumer, so its watermark is not advanced and the idempotent
// snapshot is retried. The mutex also prevents reconcile and outbox applies
// from interleaving two snapshots.
func (s *Store) reloadSystemSettings(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.systemSettingsApplyMu.Lock()
	defer s.systemSettingsApplyMu.Unlock()

	settings, err := s.db.GetSystemSettings(ctx)
	if err != nil {
		return err
	}
	if settings == nil {
		// Tests and embedders may construct a Store before provisioning the
		// singleton row. There is no committed settings snapshot to publish yet;
		// the constructor values remain authoritative until the first row/event.
		return nil
	}
	cooldown, err := s.db.GetModelCooldownSettings(ctx)
	if err != nil {
		return err
	}
	settings.RelayModelCooldownMode = cooldown.RelayMode
	settings.RelayModelCooldownSeconds = cooldown.RelaySeconds
	settings.RelayModelCooldownBackoffEnabled = cooldown.RelayBackoffEnabled
	settings.OAuthModelCooldownMode = cooldown.OAuthMode
	settings.OAuthModelCooldownSeconds = cooldown.OAuthSeconds
	settings.OAuthModelCooldownBackoffEnabled = cooldown.OAuthBackoffEnabled

	if err := s.applyStoreSystemSettings(settings); err != nil {
		return err
	}
	if s.systemSettingsApplyHook != nil {
		if err := s.systemSettingsApplyHook(ctx, settings); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyStoreSystemSettings(settings *database.SystemSettings) error {
	if s == nil || settings == nil {
		return nil
	}

	if s.GetMaxConcurrency() != settings.MaxConcurrency {
		s.SetMaxConcurrency(settings.MaxConcurrency)
	}
	s.SetMaxRetries(settings.MaxRetries)
	s.SetMaxRateLimitRetries(settings.MaxRateLimitRetries)
	s.SetRetryIntervalMS(settings.RetryIntervalMS)
	s.SetTransportRetryPolicy(settings.TransportRetryPolicy)
	s.SetTestModel(settings.TestModel)
	s.SetTestContent(settings.TestContent)
	s.SetTestConcurrency(settings.TestConcurrency)
	backgroundInterval := time.Duration(settings.BackgroundRefreshIntervalMinutes) * time.Minute
	if backgroundInterval <= 0 {
		backgroundInterval = defaultBackgroundRefreshInterval
	}
	if s.GetBackgroundRefreshInterval() != backgroundInterval {
		s.SetBackgroundRefreshInterval(backgroundInterval)
	}
	usageProbeMaxAge := time.Duration(settings.UsageProbeMaxAgeMinutes) * time.Minute
	if usageProbeMaxAge <= 0 {
		usageProbeMaxAge = defaultUsageProbeMaxAge
	}
	if s.GetUsageProbeMaxAge() != usageProbeMaxAge {
		s.SetUsageProbeMaxAge(usageProbeMaxAge)
	}
	s.SetUsageProbeConcurrency(settings.UsageProbeConcurrency)
	s.SetUsageProbeResponsesFallbackEnabled(settings.UsageProbeResponsesFallbackEnabled)
	recoveryInterval := time.Duration(settings.RecoveryProbeIntervalMinutes) * time.Minute
	if recoveryInterval <= 0 {
		recoveryInterval = defaultRecoveryProbeInterval
	}
	if s.GetRecoveryProbeInterval() != recoveryInterval {
		s.SetRecoveryProbeInterval(recoveryInterval)
	}
	if s.GetLazyMode() != settings.LazyMode {
		s.SetLazyMode(settings.LazyMode)
	}
	if s.GetProxyURL() != settings.ProxyURL {
		s.SetProxyURL(settings.ProxyURL)
	}
	s.SetAutoCleanUnauthorized(settings.AutoCleanUnauthorized)
	s.SetAutoCleanRateLimited(settings.AutoCleanRateLimited)
	s.SetAutoCleanFullUsage(settings.AutoCleanFullUsage)
	s.SetAutoCleanError(settings.AutoCleanError)
	s.SetAutoCleanExpired(settings.AutoCleanExpired)

	proxyPoolChanged := s.GetProxyPoolEnabled() != settings.ProxyPoolEnabled
	s.SetProxyPoolEnabled(settings.ProxyPoolEnabled)
	s.mu.RLock()
	proxyPoolLoaded := s.proxyPoolSet != nil && s.managedProxySet != nil
	s.mu.RUnlock()
	if settings.ProxyPoolEnabled && (proxyPoolChanged || !proxyPoolLoaded) {
		if err := s.ReloadProxyPool(); err != nil {
			return fmt.Errorf("reload enabled proxy pool: %w", err)
		}
	}

	// Deployment-level scheduler overrides remain authoritative on every reload.
	if engine := strings.TrimSpace(os.Getenv("CODEX_SCHEDULER_ENGINE")); engine != "" {
		s.SetSchedulerEngine(engine)
	} else if fastSchedulerEnabledFromEnv() {
		s.SetSchedulerEngine("indexed")
	} else {
		s.SetSchedulerEngine(settings.SchedulerEngine)
	}
	if s.GetSchedulerMode() != settings.SchedulerMode {
		s.SetSchedulerMode(settings.SchedulerMode)
	}
	if s.GetAffinityMode() != settings.AffinityMode {
		s.SetAffinityMode(settings.AffinityMode)
	}
	if s.GetSessionAffinitySpread() != settings.SessionAffinitySpread {
		s.SetSessionAffinitySpread(settings.SessionAffinitySpread)
	}
	buffer := time.Duration(database.NormalizeSessionSlotBufferSeconds(settings.SessionSlotBufferSeconds)) * time.Second
	if s.GetSessionSlotBuffer() != buffer {
		s.SetSessionSlotBuffer(buffer)
	}
	if s.SessionSlotBufferEnabled() != settings.SessionSlotBufferEnabled {
		s.SetSessionSlotBufferEnabled(settings.SessionSlotBufferEnabled)
	}

	s.SetCodexForceWebsocket(settings.CodexForceWebsocket)
	s.SetCodexOAuthKeepalive(settings.CodexOAuthKeepaliveEnabled)
	s.SetCodexRequestCompression(settings.CodexRequestCompression)
	s.SetCodexWSKeepaliveEnabled(settings.CodexWSKeepaliveEnabled)
	s.SetCodexWSKeepaliveIntervalSec(settings.CodexWSKeepaliveIntervalSec)
	s.SetCodexWSHideUpstreamErrors(settings.CodexWSHideUpstreamErrors)
	s.SetCodexWSSilentRetryEnabled(settings.CodexWSSilentRetryEnabled)
	s.SetCodexWSSilentMaxRetries(settings.CodexWSSilentMaxRetries)
	s.SetCodexFastModelAliasEnabled(settings.CodexFastModelAliasEnabled)
	s.SetCodexReasoningEffortAliasEnabled(settings.CodexReasoningEffortAliasEnabled)
	s.SetCodexFastTierInterceptEnabled(settings.CodexFastTierInterceptEnabled)
	s.SetCodexForceFastEnabled(settings.CodexForceFastEnabled)
	s.SetCodexWSSizeRouterEnabled(settings.CodexWSSizeRouterEnabled)
	s.SetCodexWSBusyAcquireMaxWaitSec(settings.CodexWSBusyAcquireMaxWaitSec)
	s.SetCodexWSBusyOverflowEnabled(settings.CodexWSBusyOverflowEnabled)
	s.SetCodexWSBusyPatienceSec(settings.CodexWSBusyPatienceSec)
	s.SetCodexWSStatelessSlots(settings.CodexWSStatelessSlots)
	s.SetGithubToken(settings.GithubToken)
	s.SetGithubProxyURL(settings.GithubProxyURL)
	s.SetOverflowAutoCompactEnabled(settings.OverflowAutoCompactEnabled)
	s.SetCompactViaResponsesEnabled(settings.CompactViaResponsesEnabled)
	s.SetCodexPreflightSSEPassthroughEnabled(settings.CodexPreflightSSEPassthroughEnabled)
	s.SetFirstTokenExcludesWsAcquire(settings.FirstTokenExcludesWsAcquire)
	s.SetCodexContinueThinkingEnabled(settings.CodexContinueThinkingEnabled)
	s.SetCodexContinueMaxRounds(settings.CodexContinueMaxRounds)
	s.SetCodexCLIVersionSyncEnabled(settings.CodexCLIVersionSyncEnabled)
	s.SetCodexCLIVersionSyncIntervalHours(settings.CodexCLIVersionSyncIntervalHours)
	s.SetCodexFingerprintDefaultMode(settings.CodexFingerprintDefaultMode)
	applyClaudeConfigToStore(s, settings.ClaudeConfig)

	s.SetAllowRemoteMigration(settings.AllowRemoteMigration)
	s.SetModelMapping(settings.ModelMapping)
	s.SetCodexModelMapping(settings.CodexModelMapping)
	s.SetPayloadRules(settings.PayloadRules)
	s.SetReasoningEffortModels(settings.ReasoningEffortModels)
	s.SetGrokAffinityMode(grokAffinityModeFromConfig(settings.GrokConfig))
	s.SetGrokProbeConfig(grokProbeConfigFromConfig(settings.GrokConfig))
	s.SetGrokMaxRateLimitRetries(grokMaxRateLimitRetriesFromConfig(settings.GrokConfig))
	s.SetGrokFollowUpEffortConfig(GrokFollowUpEffortConfigFromJSON(settings.GrokConfig))
	SetConfiguredGrokOAuthClientID(grokOAuthClientIDFromConfig(settings.GrokConfig))

	promptFilterConfig, advancedRaw := promptFilterConfigFromSettings(settings)
	if err := s.SetPromptFilterConfigWithAdvancedRaw(promptFilterConfig, advancedRaw); err != nil {
		return fmt.Errorf("apply prompt filter settings: %w", err)
	}
	if s.IgnoreUsageLimitStatus() != settings.IgnoreUsageLimitStatus {
		s.SetIgnoreUsageLimitStatus(settings.IgnoreUsageLimitStatus)
	}
	if s.GetGlobalAutoPause5hThreshold() != settings.AutoPause5hThreshold ||
		s.GetGlobalAutoPause7dThreshold() != settings.AutoPause7dThreshold {
		s.SetGlobalAutoPauseThresholds(settings.AutoPause5hThreshold, settings.AutoPause7dThreshold)
	}
	if s.GetAutoPause5hGuardBandPercent() != settings.AutoPause5hGuardBandPercent {
		s.SetAutoPause5hGuardBandPercent(settings.AutoPause5hGuardBandPercent)
	}
	if s.GetAutoPause5hGuardConcurrency() != settings.AutoPause5hGuardConcurrency {
		s.SetAutoPause5hGuardConcurrency(settings.AutoPause5hGuardConcurrency)
	}
	if s.GetSmartPacingEnabled() != settings.SmartPacingEnabled {
		s.SetSmartPacingEnabled(settings.SmartPacingEnabled)
	}
	if s.GetSmartPacingMinConcurrency() != settings.SmartPacingMinConcurrency {
		s.SetSmartPacingMinConcurrency(settings.SmartPacingMinConcurrency)
	}
	if s.GetSmartPacingWindows() != settings.SmartPacingWindows {
		s.SetSmartPacingWindows(settings.SmartPacingWindows)
	}
	modelCooldownSettings := database.ModelCooldownSettings{
		RelayMode:           settings.RelayModelCooldownMode,
		RelaySeconds:        settings.RelayModelCooldownSeconds,
		RelayBackoffEnabled: settings.RelayModelCooldownBackoffEnabled,
		OAuthMode:           settings.OAuthModelCooldownMode,
		OAuthSeconds:        settings.OAuthModelCooldownSeconds,
		OAuthBackoffEnabled: settings.OAuthModelCooldownBackoffEnabled,
	}
	if s.GetModelCooldownSettings() != database.NormalizeModelCooldownSettings(modelCooldownSettings) {
		s.SetModelCooldownSettings(modelCooldownSettings)
	}
	return nil
}
