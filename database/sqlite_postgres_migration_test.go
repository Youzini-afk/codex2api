package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"
)

func TestDiscoverMigrationSourceTablesAndColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db := openWritableSQLiteForMigrationTest(t, path)
	mustExecMigrationTest(t, db, `CREATE TABLE accounts (id INTEGER PRIMARY KEY, credentials TEXT, enabled INTEGER)`)
	mustExecMigrationTest(t, db, `CREATE TABLE data_migrations (version TEXT PRIMARY KEY)`)
	mustExecMigrationTest(t, db, `CREATE TABLE prompt_filter_secrets (id INTEGER PRIMARY KEY, secret TEXT)`)
	mustExecMigrationTest(t, db, `INSERT INTO accounts (id, credentials, enabled) VALUES (7, '{"token":"secret"}', 1)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	readOnly, err := openReadOnlyMigrationSQLite(path)
	if err != nil {
		t.Fatalf("openReadOnlyMigrationSQLite: %v", err)
	}
	defer readOnly.Close()
	tx, err := readOnly.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	tables, err := discoverMigrationSourceTables(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tables["accounts"]; !ok {
		t.Fatalf("accounts not discovered: %#v", tables)
	}
	if _, ok := tables["data_migrations"]; ok {
		t.Fatal("data_migrations must not be copied")
	}
	if _, ok := tables["prompt_filter_secrets"]; ok {
		t.Fatal("legacy prompt_filter_secrets must not be copied")
	}
	columns, err := loadMigrationSourceColumns(context.Background(), tx, "accounts")
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{columns[0].Name, columns[1].Name, columns[2].Name}; strings.Join(got, ",") != "id,credentials,enabled" {
		t.Fatalf("columns = %v", got)
	}
}

func TestIntersectMigrationColumnsUsesTargetOrder(t *testing.T) {
	source := []migrationSourceColumn{{Name: "enabled"}, {Name: "id"}, {Name: "legacy_only"}}
	target := []migrationTargetColumn{{Name: "id"}, {Name: "name"}, {Name: "enabled"}}
	got := intersectMigrationColumns(source, target)
	if len(got) != 2 || got[0].Name != "id" || got[1].Name != "enabled" {
		t.Fatalf("intersection = %#v", got)
	}
}

func TestWithPostgresSchemaDSN(t *testing.T) {
	urlDSN, err := withPostgresSchemaDSN("postgresql://user:pass@example.test/db?sslmode=require&options=-c%20statement_timeout%3D5s", "Tenant_1")
	if err != nil {
		t.Fatal(err)
	}
	parsedURL, err := url.Parse(urlDSN)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := parsedURL.Query().Get("options"), `-c statement_timeout=5s -c search_path="Tenant_1",public`; got != want {
		t.Fatalf("URL DSN decoded options=%q, want %q (dsn=%s)", got, want, urlDSN)
	}
	if _, err := pq.NewConnector(urlDSN); err != nil {
		t.Fatalf("generated URL DSN rejected by lib/pq: %v", err)
	}
	keywordDSN, err := withPostgresSchemaDSN(`host=localhost dbname=codex options='-c statement_timeout=5s'`, "Tenant_1")
	if err != nil {
		t.Fatal(err)
	}
	keywordOptions, found, err := postgresKeywordDSNValue(keywordDSN, "options")
	if err != nil {
		t.Fatal(err)
	}
	if want := `-c statement_timeout=5s -c search_path="Tenant_1",public`; !found || keywordOptions != want {
		t.Fatalf("keyword DSN decoded options=%q found=%v, want %q (dsn=%s)", keywordOptions, found, want, keywordDSN)
	}
	if _, err := pq.NewConnector(keywordDSN); err != nil {
		t.Fatalf("generated keyword DSN rejected by lib/pq: %v", err)
	}
	if _, err := withPostgresSchemaDSN("host=localhost", "bad;schema"); err == nil {
		t.Fatal("unsafe schema accepted")
	}
}

func TestReadOnlyMigrationSQLiteCannotWriteOrMutateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readonly.sqlite")
	db := openWritableSQLiteForMigrationTest(t, path)
	mustExecMigrationTest(t, db, `CREATE TABLE accounts (id INTEGER PRIMARY KEY, name TEXT)`)
	mustExecMigrationTest(t, db, `INSERT INTO accounts VALUES (1, 'before')`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	readOnly, err := openReadOnlyMigrationSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := readOnly.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var name string
	if err := tx.QueryRow(`SELECT name FROM accounts WHERE id=1`).Scan(&name); err != nil || name != "before" {
		t.Fatalf("read snapshot: name=%q err=%v", name, err)
	}
	if _, err := tx.Exec(`UPDATE accounts SET name='after' WHERE id=1`); err == nil {
		t.Fatal("read-only migration source unexpectedly accepted UPDATE")
	}
	_ = tx.Rollback()
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("source file changed: before=(%d,%v) after=(%d,%v)", before.Size(), before.ModTime(), after.Size(), after.ModTime())
	}
}

func TestMigrationBaselineHasDataRejectsDefaultZeroRow(t *testing.T) {
	db := openWritableSQLiteForMigrationTest(t, filepath.Join(t.TempDir(), "baseline.sqlite"))
	defer db.Close()
	mustExecMigrationTest(t, db, `CREATE TABLE usage_stats_baseline (id INTEGER PRIMARY KEY, total_requests INTEGER DEFAULT 0, account_billed REAL DEFAULT 0)`)
	mustExecMigrationTest(t, db, `INSERT INTO usage_stats_baseline (id) VALUES (1)`)
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	tables, err := discoverMigrationSourceTables(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	hasData, err := migrationBaselineHasData(context.Background(), tx, tables)
	if err != nil {
		t.Fatal(err)
	}
	if hasData {
		t.Fatal("all-zero default baseline must not make an empty source valid")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE usage_stats_baseline SET total_requests=3 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	hasData, err = migrationBaselineHasData(context.Background(), tx, tables)
	if err != nil || !hasData {
		t.Fatalf("non-zero baseline: hasData=%v err=%v", hasData, err)
	}
}

func TestConvertMigrationValue(t *testing.T) {
	boolean := migrationTargetColumn{DataType: "boolean", UDTName: "bool"}
	for _, test := range []struct {
		input interface{}
		want  bool
	}{{int64(0), false}, {int64(1), true}, {"false", false}, {[]byte("1"), true}} {
		input, want := test.input, test.want
		got, err := convertMigrationValue(input, boolean)
		if err != nil || got != want {
			t.Fatalf("bool %v (%T): got=%v err=%v", input, input, got, err)
		}
	}
	if _, err := convertMigrationValue(int64(2), boolean); err == nil {
		t.Fatal("invalid SQLite boolean 2 accepted")
	}

	jsonColumn := migrationTargetColumn{DataType: "jsonb", UDTName: "jsonb"}
	if got, err := convertMigrationValue([]byte(`{"a":1}`), jsonColumn); err != nil || got != `{"a":1}` {
		t.Fatalf("JSON conversion got=%v err=%v", got, err)
	}
	if _, err := convertMigrationValue("{", jsonColumn); err == nil {
		t.Fatal("invalid JSON accepted")
	}

	timestamp := migrationTargetColumn{DataType: "timestamp with time zone", UDTName: "timestamptz"}
	got, err := convertMigrationValue("2026-08-05 12:34:56", timestamp)
	if err != nil || !got.(time.Time).Equal(time.Date(2026, 8, 5, 12, 34, 56, 0, time.UTC)) {
		t.Fatalf("timestamp got=%v err=%v", got, err)
	}
	if got, err := convertMigrationValue(nil, timestamp); err != nil || got != nil {
		t.Fatalf("NULL got=%v err=%v", got, err)
	}
	bytea := migrationTargetColumn{DataType: "bytea", UDTName: "bytea"}
	if got, err := convertMigrationValue([]byte{0, 1, 255}, bytea); err != nil || string(got.([]byte)) != string([]byte{0, 1, 255}) {
		t.Fatalf("BLOB got=%v err=%v", got, err)
	}
	numeric := migrationTargetColumn{DataType: "numeric", UDTName: "numeric"}
	if got, err := convertMigrationValue("123.450", numeric); err != nil || got != "123.450" {
		t.Fatalf("numeric got=%v err=%v", got, err)
	}
}

func TestRunDataMigrationsInTxForcesRowsImportedAfterMarkers(t *testing.T) {
	db, err := New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	result, err := db.conn.ExecContext(ctx, `INSERT INTO accounts (name, platform, credentials) VALUES ('xai', 'xai', '{}')`)
	if err != nil {
		t.Fatal(err)
	}
	accountID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.ExecContext(ctx, `INSERT INTO usage_logs (account_id, endpoint, channel) VALUES ($1, '/v1/responses', '')`, accountID); err != nil {
		t.Fatal(err)
	}

	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := db.runDataMigrationsInTx(ctx, tx, true); err != nil {
		t.Fatal(err)
	}
	var channel string
	if err := tx.QueryRowContext(ctx, `SELECT channel FROM usage_logs WHERE account_id=$1`, accountID).Scan(&channel); err != nil {
		t.Fatal(err)
	}
	if channel != "grok" {
		t.Fatalf("forced imported usage channel=%q, want grok", channel)
	}
	var markerCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM data_migrations WHERE version IN ($1,$2,$3,$4)`,
		dataMigrationOAuthIdentityDedupeV1, dataMigrationOAuthIdentityDedupeV2, dataMigrationUsageLogChannelV1, dataMigrationWorkspaceIdentityV3).Scan(&markerCount); err != nil {
		t.Fatal(err)
	}
	if markerCount != 4 {
		t.Fatalf("forced migration marker count=%d, want 4", markerCount)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestPromptNormalizationInTxCoversImportedAndLegacyRows(t *testing.T) {
	db, err := New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.conn.ExecContext(ctx, `INSERT INTO prompt_policy_incidents (id, incident_id, local_evaluation_state, local_outcome, prompt_text, local_comparison) VALUES (1, 'imported', 'completed', 'no_hit', 'evidence', '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.ExecContext(ctx, `INSERT INTO prompt_filter_logs (id, source, error_code, full_text) VALUES (88, 'upstream_cyber_policy', 'cyber_policy', 'legacy evidence')`); err != nil {
		t.Fatal(err)
	}

	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := normalizePromptPolicyIncidentData(ctx, tx, true); err != nil {
		t.Fatal(err)
	}
	var promptAvailable bool
	var comparison string
	if err := tx.QueryRowContext(ctx, `SELECT prompt_available, local_comparison FROM prompt_policy_incidents WHERE incident_id='imported'`).Scan(&promptAvailable, &comparison); err != nil {
		t.Fatal(err)
	}
	if !promptAvailable || comparison != PromptPolicyComparisonUpstreamOnly {
		t.Fatalf("imported prompt available=%v comparison=%q", promptAvailable, comparison)
	}
	var legacyID int64
	var legacyComparison string
	if err := tx.QueryRowContext(ctx, `SELECT id, local_comparison FROM prompt_policy_incidents WHERE incident_id='legacy-88'`).Scan(&legacyID, &legacyComparison); err != nil {
		t.Fatal(err)
	}
	if legacyID != 2 || legacyComparison != PromptPolicyComparisonLegacyUnknown {
		t.Fatalf("legacy prompt id=%d comparison=%q, want id=2 legacy_unknown", legacyID, legacyComparison)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteToPostgresAutoMigrationIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CODEX2API_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("CODEX2API_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sourcePath := filepath.Join(t.TempDir(), "source.sqlite")
	createMigrationIntegrationSource(t, sourcePath, false)

	schema := fmt.Sprintf("SQLite_Migration_%d", time.Now().UnixNano())
	cleanupPostgresMigrationSchema(t, dsn, schema)
	defer cleanupPostgresMigrationSchema(t, dsn, schema)
	options := Options{Schema: schema, AutoMigrateFromSQLite: true, SQLiteMigrationSourcePath: sourcePath}
	db, err := NewWithOptions("postgres", dsn, options)
	if err != nil {
		t.Fatalf("NewWithOptions migration: %v", err)
	}
	// Force the initialized connection out of the idle pool so this assertion
	// exercises the DSN startup options on a newly opened physical connection.
	db.conn.SetMaxIdleConns(0)
	var freshSchema string
	var freshAccountCount int
	if err := db.conn.QueryRowContext(ctx, `SELECT current_schema(), (SELECT COUNT(*) FROM accounts)`).Scan(&freshSchema, &freshAccountCount); err != nil {
		t.Fatalf("unqualified query on fresh uppercase-schema connection: %v", err)
	}
	if freshSchema != schema || freshAccountCount != 6 {
		t.Fatalf("fresh connection schema=%q accounts=%d, want schema=%q accounts=6", freshSchema, freshAccountCount, schema)
	}
	db.conn.SetMaxIdleConns(50)

	var (
		credentials string
		enabled     bool
		locked      bool
		cooldown    sql.NullTime
		createdAt   time.Time
	)
	if err := db.conn.QueryRowContext(ctx, `SELECT credentials::text, enabled, locked, cooldown_until, created_at FROM `+qualifiedMigrationTable(schema, "accounts")+` WHERE id=41`).Scan(&credentials, &enabled, &locked, &cooldown, &createdAt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(credentials, `"refresh_token": "secret-token"`) || !enabled || locked || cooldown.Valid {
		t.Fatalf("account conversion credentials=%s enabled=%v locked=%v cooldown=%v", credentials, enabled, locked, cooldown)
	}
	if !createdAt.Equal(time.Date(2026, 8, 5, 12, 34, 56, 0, time.UTC)) {
		t.Fatalf("created_at=%v", createdAt)
	}
	var usageID int64
	var stream bool
	var errorMessage sql.NullString
	if err := db.conn.QueryRowContext(ctx, `SELECT id, stream, error_message FROM `+qualifiedMigrationTable(schema, "usage_logs")+` WHERE id=77`).Scan(&usageID, &stream, &errorMessage); err != nil {
		t.Fatal(err)
	}
	if usageID != 77 || !stream || errorMessage.Valid {
		t.Fatalf("usage id=%d stream=%v error=%v", usageID, stream, errorMessage)
	}
	rows, err := db.conn.QueryContext(ctx, `SELECT id, channel FROM `+qualifiedMigrationTable(schema, "usage_logs")+` WHERE id IN (77, 78) ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	channels := make(map[int64]string, 2)
	for rows.Next() {
		var id int64
		var channel string
		if err := rows.Scan(&id, &channel); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		channels[id] = channel
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if channels[77] != "codex" || channels[78] != "grok" {
		t.Fatalf("usage channels=%v, want 77=codex 78=grok", channels)
	}
	var siteName string
	var promptEnabled bool
	if err := db.conn.QueryRowContext(ctx, `SELECT site_name, prompt_filter_enabled FROM `+qualifiedMigrationTable(schema, "system_settings")+` WHERE id=1`).Scan(&siteName, &promptEnabled); err != nil {
		t.Fatal(err)
	}
	if siteName != "Migrated Site" || !promptEnabled {
		t.Fatalf("settings site=%q enabled=%v", siteName, promptEnabled)
	}
	var isPerson bool
	if err := db.conn.QueryRowContext(ctx, `SELECT is_person FROM `+qualifiedMigrationTable(schema, "prompt_risk_events")+` WHERE id=9`).Scan(&isPerson); err != nil || !isPerson {
		t.Fatalf("risk is_person=%v err=%v", isPerson, err)
	}
	var policyMode, policyProfile string
	if err := db.conn.QueryRowContext(ctx, `SELECT policy_mode, policy_profile FROM `+qualifiedMigrationTable(schema, "prompt_filter_newapi_bindings")+` WHERE api_key_id=7`).Scan(&policyMode, &policyProfile); err != nil {
		t.Fatal(err)
	}
	if policyMode != PromptFilterPolicyModeInherit || policyProfile != PromptFilterPolicyProfileInherit {
		t.Fatalf("imported NewAPI policy mode=%q profile=%q", policyMode, policyProfile)
	}
	var promptAvailable bool
	var comparison string
	if err := db.conn.QueryRowContext(ctx, `SELECT prompt_available, local_comparison FROM `+qualifiedMigrationTable(schema, "prompt_policy_incidents")+` WHERE incident_id='imported-incident'`).Scan(&promptAvailable, &comparison); err != nil {
		t.Fatal(err)
	}
	if !promptAvailable || comparison != PromptPolicyComparisonUpstreamOnly {
		t.Fatalf("imported incident available=%v comparison=%q", promptAvailable, comparison)
	}
	var legacyState, legacyComparison string
	if err := db.conn.QueryRowContext(ctx, `SELECT local_evaluation_state, local_comparison FROM `+qualifiedMigrationTable(schema, "prompt_policy_incidents")+` WHERE incident_id='legacy-88'`).Scan(&legacyState, &legacyComparison); err != nil {
		t.Fatal(err)
	}
	if legacyState != PromptPolicyEvaluationLegacyUnknown || legacyComparison != PromptPolicyComparisonLegacyUnknown {
		t.Fatalf("legacy incident state=%q comparison=%q", legacyState, legacyComparison)
	}
	var maxIncidentID, nextIncidentID int64
	if err := db.conn.QueryRowContext(ctx, `SELECT MAX(id) FROM `+qualifiedMigrationTable(schema, "prompt_policy_incidents")).Scan(&maxIncidentID); err != nil {
		t.Fatal(err)
	}
	if err := db.conn.QueryRowContext(ctx, `INSERT INTO `+qualifiedMigrationTable(schema, "prompt_policy_incidents")+` (incident_id) VALUES ('after-legacy-sequence') RETURNING id`).Scan(&nextIncidentID); err != nil {
		t.Fatal(err)
	}
	if nextIncidentID <= maxIncidentID {
		t.Fatalf("prompt_policy_incidents next id=%d, want > imported/legacy max=%d", nextIncidentID, maxIncidentID)
	}
	var nextPromptFilterLogID int64
	if err := db.conn.QueryRowContext(ctx, `INSERT INTO `+qualifiedMigrationTable(schema, "prompt_filter_logs")+` (source) VALUES ('after-sequence') RETURNING id`).Scan(&nextPromptFilterLogID); err != nil {
		t.Fatal(err)
	}
	if nextPromptFilterLogID != 89 {
		t.Fatalf("single-row prompt_filter_logs next id=%d, want 89", nextPromptFilterLogID)
	}
	var firstEmptySequenceID int64
	if err := db.conn.QueryRowContext(ctx, `INSERT INTO `+qualifiedMigrationTable(schema, "image_generation_jobs")+` DEFAULT VALUES RETURNING id`).Scan(&firstEmptySequenceID); err != nil {
		t.Fatal(err)
	}
	if firstEmptySequenceID != 1 {
		t.Fatalf("empty image_generation_jobs sequence first id=%d, want 1", firstEmptySequenceID)
	}
	for _, ids := range [][2]int64{{43, 44}, {45, 46}} {
		var active int
		if err := db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+qualifiedMigrationTable(schema, "accounts")+` WHERE id IN ($1,$2) AND status<>'deleted'`, ids[0], ids[1]).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if active != 1 {
			t.Fatalf("dedupe pair %v active=%d, want 1", ids, active)
		}
	}
	var migrationMarkerCount int
	if err := db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+qualifiedMigrationTable(schema, "data_migrations")+` WHERE version IN ($1,$2,$3,$4)`,
		dataMigrationOAuthIdentityDedupeV1, dataMigrationOAuthIdentityDedupeV2, dataMigrationUsageLogChannelV1, dataMigrationWorkspaceIdentityV3).Scan(&migrationMarkerCount); err != nil {
		t.Fatal(err)
	}
	if migrationMarkerCount != 4 {
		t.Fatalf("current data migration markers=%d, want 4", migrationMarkerCount)
	}
	var completedMarkerCount int
	if err := db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+qualifiedMigrationTable(schema, "sqlite_postgres_migrations")+` WHERE migration_name=$1`, sqlitePostgresMigrationName).Scan(&completedMarkerCount); err != nil {
		t.Fatal(err)
	}
	if completedMarkerCount != 1 {
		t.Fatalf("completed migration markers=%d, want 1", completedMarkerCount)
	}
	var nextID int64
	if err := db.conn.QueryRowContext(ctx, `INSERT INTO `+qualifiedMigrationTable(schema, "accounts")+` (name, credentials) VALUES ('next', '{}') RETURNING id`).Scan(&nextID); err != nil {
		t.Fatal(err)
	}
	if nextID != 47 {
		t.Fatalf("accounts sequence next id=%d, want 47", nextID)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// The durable marker is checked before the non-empty-target guard.
	db, err = NewWithOptions("postgres", dsn, options)
	if err != nil {
		t.Fatalf("idempotent restart: %v", err)
	}
	var accountCount int
	if err := db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+qualifiedMigrationTable(schema, "accounts")).Scan(&accountCount); err != nil {
		t.Fatal(err)
	}
	if accountCount != 7 {
		t.Fatalf("idempotent restart account count=%d", accountCount)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAutoMigrationGuardCanonicalSchemaAndUnlockIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CODEX2API_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("CODEX2API_TEST_POSTGRES_DSN is not set")
	}
	schema := fmt.Sprintf("Guard_Schema_%d", time.Now().UnixNano())
	cleanupPostgresMigrationSchema(t, dsn, schema)
	defer cleanupPostgresMigrationSchema(t, dsn, schema)
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + pq.QuoteIdentifier(schema)); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	admin.Close()

	schemaDSN, err := withPostgresSchemaDSN(dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := sql.Open("postgres", schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := &DB{conn: pool, driver: "postgres"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	defaultGuard, err := db.beginPostgresAutoMigrationGuard(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if defaultGuard.schema != schema {
		t.Fatalf("resolved default schema=%q, want exact %q", defaultGuard.schema, schema)
	}
	keys := [2]int32{defaultGuard.lockKeyOne, defaultGuard.lockKeyTwo}
	if err := defaultGuard.close(); err != nil {
		t.Fatal(err)
	}
	explicitGuard, err := db.beginPostgresAutoMigrationGuard(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	if got := [2]int32{explicitGuard.lockKeyOne, explicitGuard.lockKeyTwo}; got != keys {
		t.Fatalf("default lock keys=%v explicit lock keys=%v", keys, got)
	}
	if err := explicitGuard.close(); err != nil {
		t.Fatal(err)
	}

	other, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	otherConn, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer otherConn.Close()
	var acquired bool
	if err := otherConn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1, $2)`, keys[0], keys[1]).Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("migration advisory lock remained held after guard.close")
	}
	var released bool
	if err := otherConn.QueryRowContext(ctx, `SELECT pg_advisory_unlock($1, $2)`, keys[0], keys[1]).Scan(&released); err != nil || !released {
		t.Fatalf("release verification lock: released=%v err=%v", released, err)
	}
}

func TestSQLiteToPostgresConcurrentAutoMigrationIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CODEX2API_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("CODEX2API_TEST_POSTGRES_DSN is not set")
	}
	sourcePath := filepath.Join(t.TempDir(), "concurrent.sqlite")
	createMigrationIntegrationSource(t, sourcePath, false)
	schema := fmt.Sprintf("Concurrent_Migration_%d", time.Now().UnixNano())
	cleanupPostgresMigrationSchema(t, dsn, schema)
	defer cleanupPostgresMigrationSchema(t, dsn, schema)
	options := Options{Schema: schema, AutoMigrateFromSQLite: true, SQLiteMigrationSourcePath: sourcePath}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			db, err := NewWithOptions("postgres", dsn, options)
			if err == nil {
				err = db.Close()
			}
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent NewWithOptions: %v", err)
		}
	}
	raw, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var accounts, usage, markers int
	if err := raw.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM `+qualifiedMigrationTable(schema, "accounts")+`), (SELECT COUNT(*) FROM `+qualifiedMigrationTable(schema, "usage_logs")+`), (SELECT COUNT(*) FROM `+qualifiedMigrationTable(schema, "sqlite_postgres_migrations")+` WHERE migration_name=$1)`, sqlitePostgresMigrationName).Scan(&accounts, &usage, &markers); err != nil {
		t.Fatal(err)
	}
	if accounts != 6 || usage != 2 || markers != 1 {
		t.Fatalf("concurrent migration accounts=%d usage=%d markers=%d, want 6/2/1", accounts, usage, markers)
	}
}

func TestSQLiteToPostgresAutoMigrationGuardsIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CODEX2API_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("CODEX2API_TEST_POSTGRES_DSN is not set")
	}
	sourcePath := filepath.Join(t.TempDir(), "source.sqlite")
	createMigrationIntegrationSource(t, sourcePath, false)

	nonEmptySchema := fmt.Sprintf("sqlite_migration_nonempty_%d", time.Now().UnixNano())
	cleanupPostgresMigrationSchema(t, dsn, nonEmptySchema)
	defer cleanupPostgresMigrationSchema(t, dsn, nonEmptySchema)
	existing, err := New("postgres", dsn, nonEmptySchema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := existing.conn.Exec(`INSERT INTO ` + qualifiedMigrationTable(nonEmptySchema, "accounts") + ` (name, credentials) VALUES ('existing', '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := existing.conn.Exec(`INSERT INTO ` + qualifiedMigrationTable(nonEmptySchema, "prompt_filter_newapi_bindings") + ` (api_key_id, platform_code, platform_name, secret, policy_mode, policy_profile) VALUES (7, 'legacy', 'Legacy', '01234567890123456789012345678901', 'shadow', 'strict')`); err != nil {
		t.Fatal(err)
	}
	var beforeDataMarkers, beforeTableCount int
	if err := existing.conn.QueryRow(`SELECT COUNT(*) FROM ` + qualifiedMigrationTable(nonEmptySchema, "data_migrations")).Scan(&beforeDataMarkers); err != nil {
		t.Fatal(err)
	}
	if err := existing.conn.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=$1`, nonEmptySchema).Scan(&beforeTableCount); err != nil {
		t.Fatal(err)
	}
	var beforeAutoMarkerTable bool
	if err := existing.conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema=$1 AND table_name='sqlite_postgres_migrations')`, nonEmptySchema).Scan(&beforeAutoMarkerTable); err != nil {
		t.Fatal(err)
	}
	if beforeAutoMarkerTable {
		t.Fatal("fresh non-empty fixture unexpectedly has an auto-migration marker table")
	}
	if err := existing.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWithOptions("postgres", dsn, Options{Schema: nonEmptySchema, AutoMigrateFromSQLite: true, SQLiteMigrationSourcePath: sourcePath}); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("non-empty target error=%v", err)
	}
	rawNonEmpty, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer rawNonEmpty.Close()
	var afterMode, afterProfile string
	if err := rawNonEmpty.QueryRow(`SELECT policy_mode, policy_profile FROM `+qualifiedMigrationTable(nonEmptySchema, "prompt_filter_newapi_bindings")+` WHERE api_key_id=7`).Scan(&afterMode, &afterProfile); err != nil {
		t.Fatal(err)
	}
	if afterMode != "shadow" || afterProfile != "strict" {
		t.Fatalf("rejected target was normalized: mode=%q profile=%q", afterMode, afterProfile)
	}
	var afterDataMarkers, afterTableCount int
	var afterAutoMarkerTable bool
	if err := rawNonEmpty.QueryRow(`SELECT COUNT(*) FROM ` + qualifiedMigrationTable(nonEmptySchema, "data_migrations")).Scan(&afterDataMarkers); err != nil {
		t.Fatal(err)
	}
	if err := rawNonEmpty.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=$1`, nonEmptySchema).Scan(&afterTableCount); err != nil {
		t.Fatal(err)
	}
	if err := rawNonEmpty.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema=$1 AND table_name='sqlite_postgres_migrations')`, nonEmptySchema).Scan(&afterAutoMarkerTable); err != nil {
		t.Fatal(err)
	}
	if afterDataMarkers != beforeDataMarkers || afterTableCount != beforeTableCount || afterAutoMarkerTable {
		t.Fatalf("rejected target side effects: data markers %d->%d tables %d->%d auto marker=%v", beforeDataMarkers, afterDataMarkers, beforeTableCount, afterTableCount, afterAutoMarkerTable)
	}

	failureSchema := fmt.Sprintf("sqlite_migration_rollback_%d", time.Now().UnixNano())
	cleanupPostgresMigrationSchema(t, dsn, failureSchema)
	defer cleanupPostgresMigrationSchema(t, dsn, failureSchema)
	badSource := filepath.Join(t.TempDir(), "bad.sqlite")
	createMigrationIntegrationSource(t, badSource, true)
	if _, err := NewWithOptions("postgres", dsn, Options{Schema: failureSchema, AutoMigrateFromSQLite: true, SQLiteMigrationSourcePath: badSource}); err == nil {
		t.Fatal("invalid JSON source unexpectedly migrated")
	}
	raw, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var accountCount int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM ` + qualifiedMigrationTable(failureSchema, "accounts")).Scan(&accountCount); err != nil {
		t.Fatal(err)
	}
	var usageCount int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM ` + qualifiedMigrationTable(failureSchema, "usage_logs")).Scan(&usageCount); err != nil {
		t.Fatal(err)
	}
	var markerTableExists bool
	if err := raw.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema=$1 AND table_name='sqlite_postgres_migrations')`, failureSchema).Scan(&markerTableExists); err != nil {
		t.Fatal(err)
	}
	if accountCount != 0 || usageCount != 0 || markerTableExists {
		t.Fatalf("failed migration was not rolled back: accounts=%d usage_logs=%d marker_table=%v", accountCount, usageCount, markerTableExists)
	}
}

func openWritableSQLiteForMigrationTest(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func mustExecMigrationTest(t *testing.T, db *sql.DB, query string, args ...interface{}) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %s: %v", query, err)
	}
}

func createMigrationIntegrationSource(t *testing.T, path string, invalidJSON bool) {
	t.Helper()
	db := openWritableSQLiteForMigrationTest(t, path)
	defer db.Close()
	statements := []string{
		`CREATE TABLE accounts (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, platform TEXT, type TEXT, credentials TEXT NOT NULL, proxy_url TEXT, status TEXT, cooldown_until TIMESTAMP NULL, enabled INTEGER, locked INTEGER, created_at TIMESTAMP, updated_at TIMESTAMP)`,
		`CREATE TABLE usage_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, account_id INTEGER, endpoint TEXT, model TEXT, stream INTEGER, error_message TEXT NULL, created_at TIMESTAMP)`,
		`CREATE TABLE system_settings (id INTEGER PRIMARY KEY, site_name TEXT, max_concurrency INTEGER, prompt_filter_enabled INTEGER)`,
		`CREATE TABLE prompt_risk_events (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TIMESTAMP, source_type TEXT NOT NULL, source_id TEXT NOT NULL, subject_type TEXT NOT NULL, subject_key TEXT NOT NULL, is_person INTEGER, event_kind TEXT NOT NULL)`,
		`CREATE TABLE prompt_filter_newapi_bindings (api_key_id INTEGER PRIMARY KEY, platform_code TEXT NOT NULL, platform_name TEXT NOT NULL, secret TEXT NOT NULL, enabled INTEGER, require_signed_identity INTEGER, policy_mode TEXT, policy_profile TEXT, updated_at TIMESTAMP)`,
		`CREATE TABLE prompt_policy_incidents (id INTEGER PRIMARY KEY AUTOINCREMENT, incident_id TEXT NOT NULL UNIQUE, created_at TIMESTAMP, local_evaluation_state TEXT, local_outcome TEXT, prompt_text TEXT, local_comparison TEXT)`,
		`CREATE TABLE prompt_filter_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TIMESTAMP, source TEXT, endpoint TEXT, request_protocol TEXT, request_provider TEXT, model TEXT, api_key_id INTEGER, api_key_name TEXT, api_key_masked TEXT, error_code TEXT, full_text TEXT)`,
	}
	for _, statement := range statements {
		mustExecMigrationTest(t, db, statement)
	}
	credentials := `{"refresh_token":"secret-token","nested":{"ok":true}}`
	mustExecMigrationTest(t, db, `INSERT INTO accounts (id, name, platform, type, credentials, proxy_url, status, cooldown_until, enabled, locked, created_at, updated_at) VALUES (41, 'legacy', 'openai', 'oauth', ?, '', 'active', NULL, 1, 0, '2026-08-05 12:34:56', '2026-08-05T12:35:56Z')`, credentials)
	mustExecMigrationTest(t, db, `INSERT INTO accounts (id, name, platform, type, credentials, proxy_url, status, cooldown_until, enabled, locked, created_at, updated_at) VALUES (42, 'grok', 'xai', 'api', '{"api_key":"xai-secret"}', '', 'active', NULL, 1, 0, '2026-08-05T12:34:56Z', '2026-08-05T12:35:56Z')`)
	mustExecMigrationTest(t, db, `INSERT INTO accounts (id, name, platform, type, credentials, proxy_url, status, cooldown_until, enabled, locked, created_at, updated_at) VALUES (43, 'oauth-old', 'openai', 'oauth', '{"email":"dup@example.com","account_id":"same-oauth","access_token":"old"}', '', 'active', NULL, 1, 0, '2026-08-01T12:34:56Z', '2026-08-01T12:35:56Z')`)
	mustExecMigrationTest(t, db, `INSERT INTO accounts (id, name, platform, type, credentials, proxy_url, status, cooldown_until, enabled, locked, created_at, updated_at) VALUES (44, 'oauth-new', 'openai', 'oauth', '{"email":"dup@example.com","account_id":"same-oauth","access_token":"new","refresh_token":"refresh"}', '', 'active', NULL, 1, 0, '2026-08-02T12:34:56Z', '2026-08-02T12:35:56Z')`)
	mustExecMigrationTest(t, db, `INSERT INTO accounts (id, name, platform, type, credentials, proxy_url, status, cooldown_until, enabled, locked, created_at, updated_at) VALUES (45, 'workspace-old', 'openai', 'oauth', '{"email":"workspace@example.com","workspace_id":"ws-1","account_id":"first"}', '', 'active', NULL, 1, 0, '2026-08-03T12:34:56Z', '2026-08-03T12:35:56Z')`)
	mustExecMigrationTest(t, db, `INSERT INTO accounts (id, name, platform, type, credentials, proxy_url, status, cooldown_until, enabled, locked, created_at, updated_at) VALUES (46, 'workspace-new', 'openai', 'oauth', '{"email":"workspace@example.com","workspace_id":"ws-1","account_id":"second"}', '', 'active', NULL, 1, 0, '2026-08-04T12:34:56Z', '2026-08-04T12:35:56Z')`)
	mustExecMigrationTest(t, db, `INSERT INTO usage_logs (id, account_id, endpoint, model, stream, error_message, created_at) VALUES (77, 41, '/v1/responses', 'gpt-5.4', 1, NULL, '2026-08-05T12:40:00Z')`)
	mustExecMigrationTest(t, db, `INSERT INTO usage_logs (id, account_id, endpoint, model, stream, error_message, created_at) VALUES (78, 42, '/v1/responses', 'grok-4', 0, NULL, '2026-08-05T12:40:01Z')`)
	mustExecMigrationTest(t, db, `INSERT INTO system_settings (id, site_name, max_concurrency, prompt_filter_enabled) VALUES (1, 'Migrated Site', 9, 1)`)
	mustExecMigrationTest(t, db, `INSERT INTO prompt_risk_events (id, created_at, source_type, source_id, subject_type, subject_key, is_person, event_kind) VALUES (9, '2026-08-05T12:41:00Z', 'incident', 'source-1', 'newapi_user', 'user-1', 1, 'blocked')`)
	mustExecMigrationTest(t, db, `INSERT INTO prompt_filter_newapi_bindings (api_key_id, platform_code, platform_name, secret, enabled, require_signed_identity, policy_mode, policy_profile, updated_at) VALUES (7, 'legacy', 'Legacy', '01234567890123456789012345678901', 1, 0, 'shadow', 'strict', '2026-08-05T12:42:00Z')`)
	mustExecMigrationTest(t, db, `INSERT INTO prompt_policy_incidents (id, incident_id, created_at, local_evaluation_state, local_outcome, prompt_text, local_comparison) VALUES (1, 'imported-incident', '2026-08-05T12:43:00Z', 'completed', 'no_hit', 'available evidence', '')`)
	mustExecMigrationTest(t, db, `INSERT INTO prompt_filter_logs (id, created_at, source, endpoint, request_protocol, request_provider, model, api_key_id, api_key_name, api_key_masked, error_code, full_text) VALUES (88, '2026-08-05T12:44:00Z', 'upstream_cyber_policy', '/v1/responses', 'responses', 'openai', 'gpt-5.4', 0, '', '', 'cyber_policy', 'legacy full text')`)
	if invalidJSON {
		// api_keys is deliberately copied after accounts and usage_logs. This
		// conversion failure therefore proves earlier successful INSERT batches
		// are rolled back with the rest of the target transaction.
		mustExecMigrationTest(t, db, `CREATE TABLE api_keys (id INTEGER PRIMARY KEY AUTOINCREMENT, key TEXT NOT NULL UNIQUE, allowed_group_ids TEXT)`)
		mustExecMigrationTest(t, db, `INSERT INTO api_keys (id, key, allowed_group_ids) VALUES (5, 'bad-json-key', '{')`)
	}
}

func cleanupPostgresMigrationSchema(t *testing.T, dsn, schema string) {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Logf("open PostgreSQL for cleanup: %v", err)
		return
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+pq.QuoteIdentifier(schema)+` CASCADE`); err != nil {
		t.Logf("cleanup schema %s: %v", schema, err)
	}
}
