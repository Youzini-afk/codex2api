package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/lib/pq"
)

const sqlitePostgresMigrationName = "sqlite-to-postgres-v1"

var postgresSchemaNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Options controls optional database initialization behavior. The zero value
// preserves the historical New behavior.
type Options struct {
	Schema                    string
	AutoMigrateFromSQLite     bool
	SQLiteMigrationSourcePath string
}

// sqlitePostgresBusinessTables is both the migration allow-list and the copy
// order. Parent/singleton tables precede child and high-volume audit tables.
var sqlitePostgresBusinessTables = []string{
	"accounts",
	"account_groups",
	"proxies",
	"system_settings",
	"model_registry",
	"model_registry_sync",
	"usage_stats_baseline",
	"image_prompt_templates",
	"prompt_rule_candidates",
	"account_group_members",
	"account_model_cooldowns",
	"usage_logs",
	"api_keys",
	"api_key_scope_counters",
	"account_events",
	"image_generation_jobs",
	"image_assets",
	"prompt_filter_logs",
	"prompt_filter_newapi_bindings",
	"prompt_rule_candidate_evidence",
	"prompt_policy_incidents",
	"prompt_risk_event_sources",
	"prompt_risk_identities",
	"prompt_risk_events",
	"prompt_risk_trust_policies",
	"prompt_risk_trust_events",
}

var sqlitePostgresBusinessTableSet = func() map[string]struct{} {
	result := make(map[string]struct{}, len(sqlitePostgresBusinessTables))
	for _, table := range sqlitePostgresBusinessTables {
		result[table] = struct{}{}
	}
	return result
}()

type migrationTargetColumn struct {
	Name       string
	DataType   string
	UDTName    string
	Default    sql.NullString
	IsIdentity bool
}

type migrationSourceColumn struct {
	Name string
	Type string
}

func withPostgresSchemaDSN(dsn, schema string) (string, error) {
	schema = strings.TrimSpace(schema)
	if schema == "" {
		return dsn, nil
	}
	if len(schema) > 63 || !postgresSchemaNamePattern.MatchString(schema) {
		return "", fmt.Errorf("invalid PostgreSQL schema %q", schema)
	}
	// search_path is parsed as a PostgreSQL identifier list, so an unquoted
	// mixed-case schema would be folded to lower case. Quote the configured
	// identifier exactly as the CREATE SCHEMA path does.
	option := "-c search_path=" + pq.QuoteIdentifier(schema) + ",public"
	trimmedDSN := strings.TrimSpace(dsn)
	if strings.HasPrefix(trimmedDSN, "postgres://") || strings.HasPrefix(trimmedDSN, "postgresql://") {
		parsed, err := url.Parse(trimmedDSN)
		if err != nil {
			return "", fmt.Errorf("parse PostgreSQL DSN: %w", err)
		}
		query := parsed.Query()
		if existing := strings.TrimSpace(query.Get("options")); existing != "" {
			option = existing + " " + option
		}
		query.Set("options", option)
		parsed.RawQuery = query.Encode()
		return parsed.String(), nil
	}
	existing, _, err := postgresKeywordDSNValue(dsn, "options")
	if err != nil {
		return "", fmt.Errorf("parse PostgreSQL keyword DSN: %w", err)
	}
	if strings.TrimSpace(existing) != "" {
		option = existing + " " + option
	}
	return strings.TrimSpace(dsn) + " options=" + quotePostgresKeywordDSNValue(option), nil
}

func quotePostgresKeywordDSNValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `'`, `\'`)
	return `'` + value + `'`
}

// postgresKeywordDSNValue reads libpq's keyword/value DSN syntax, including
// quoted values and backslash escapes. The caller appends a final options
// parameter, whose merged value therefore remains effective even when the
// original DSN already contained options.
func postgresKeywordDSNValue(dsn, wanted string) (string, bool, error) {
	var result string
	found := false
	runes := []rune(dsn)
	index := 0
	next := func() (rune, bool) {
		if index >= len(runes) {
			return 0, false
		}
		value := runes[index]
		index++
		return value, true
	}
	skipSpaces := func() (rune, bool) {
		value, ok := next()
		for ok && unicode.IsSpace(value) {
			value, ok = next()
		}
		return value, ok
	}
	for {
		value, ok := skipSpaces()
		if !ok {
			return result, found, nil
		}

		var key []rune
		for !unicode.IsSpace(value) && value != '=' {
			key = append(key, value)
			value, ok = next()
			if !ok {
				break
			}
		}
		if value != '=' {
			value, ok = skipSpaces()
		}
		if len(key) == 0 || !ok || value != '=' {
			return "", false, fmt.Errorf("missing = after %q", string(key))
		}
		value, ok = skipSpaces()
		if !ok {
			if string(key) == wanted {
				result = ""
				found = true
			}
			return result, found, nil
		}

		var parsedValue []rune
		if value == '\'' {
			for {
				value, ok = next()
				if !ok {
					return "", false, errors.New("unterminated quoted value")
				}
				if value == '\'' {
					break
				}
				if value == '\\' {
					value, ok = next()
					if !ok {
						return "", false, errors.New("trailing backslash in quoted value")
					}
				}
				parsedValue = append(parsedValue, value)
			}
		} else {
			for !unicode.IsSpace(value) {
				if value == '\\' {
					value, ok = next()
					if !ok {
						return "", false, errors.New("trailing backslash in value")
					}
				}
				parsedValue = append(parsedValue, value)
				value, ok = next()
				if !ok {
					break
				}
			}
		}
		if string(key) == wanted {
			result = string(parsedValue)
			found = true
		}
	}
}

func validateSQLiteMigrationSource(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("SQLite migration source path is empty")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve SQLite migration source: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("SQLite migration source does not exist: %s", absPath)
		}
		return "", fmt.Errorf("stat SQLite migration source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("SQLite migration source is not a regular file: %s", absPath)
	}
	return absPath, nil
}

func openReadOnlyMigrationSQLite(path string) (*sql.DB, error) {
	absPath, err := validateSQLiteMigrationSource(path)
	if err != nil {
		return nil, err
	}
	escapedPath := (&url.URL{Path: filepath.ToSlash(absPath)}).EscapedPath()
	query := url.Values{}
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", sqliteBusyTimeoutMillis))

	source, err := sql.Open("sqlite", "file:"+escapedPath+"?"+query.Encode())
	if err != nil {
		return nil, fmt.Errorf("open SQLite migration source: %w", err)
	}
	source.SetMaxOpenConns(1)
	source.SetMaxIdleConns(1)
	return source, nil
}

type postgresAutoMigrationGuard struct {
	conn       *sql.Conn
	schema     string
	lockKeyOne int32
	lockKeyTwo int32
	locked     bool
	completed  bool
}

func (guard *postgresAutoMigrationGuard) close() error {
	if guard == nil || guard.conn == nil {
		return nil
	}
	conn := guard.conn
	guard.conn = nil
	if guard.locked {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var unlocked bool
		err := conn.QueryRowContext(unlockCtx, `SELECT pg_advisory_unlock($1, $2)`, guard.lockKeyOne, guard.lockKeyTwo).Scan(&unlocked)
		cancel()
		if err != nil || !unlocked {
			// A session-level advisory lock must never return to the pool when
			// unlock cannot be confirmed. driver.ErrBadConn makes database/sql
			// discard the underlying physical connection.
			_ = conn.Raw(func(interface{}) error { return driver.ErrBadConn })
			_ = conn.Close()
			if err != nil {
				return fmt.Errorf("release PostgreSQL migration advisory lock: %w", err)
			}
			return errors.New("release PostgreSQL migration advisory lock: lock was not held by the reserved session")
		}
		guard.locked = false
	}
	return conn.Close()
}

// beginPostgresAutoMigrationGuard takes a session-scoped advisory lock on a
// dedicated connection before inspecting or mutating the target. The preflight
// deliberately uses only catalog reads and SELECTs: a rejected non-empty
// target must not gain a schema, a table, a marker, or normalized business
// values merely because the one-shot flag was enabled by mistake.
func (db *DB) beginPostgresAutoMigrationGuard(ctx context.Context, configuredSchema string) (*postgresAutoMigrationGuard, error) {
	if db == nil || db.conn == nil || db.isSQLite() {
		return nil, errors.New("SQLite to PostgreSQL auto-migration requires the postgres driver")
	}
	configuredSchema = strings.TrimSpace(configuredSchema)
	if configuredSchema != "" && (len(configuredSchema) > 63 || !postgresSchemaNamePattern.MatchString(configuredSchema)) {
		return nil, fmt.Errorf("invalid PostgreSQL migration schema %q", configuredSchema)
	}

	session, err := db.conn.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("reserve PostgreSQL migration session: %w", err)
	}
	guard := &postgresAutoMigrationGuard{conn: session}
	cleanup := true
	defer func() {
		if cleanup {
			guard.close()
		}
	}()

	guard.schema, err = resolveMigrationTargetSchema(ctx, session, configuredSchema)
	if err != nil {
		return nil, err
	}
	if err := session.QueryRowContext(ctx, `SELECT hashtext($1), hashtext($2)`, sqlitePostgresMigrationName, guard.schema).Scan(&guard.lockKeyOne, &guard.lockKeyTwo); err != nil {
		return nil, fmt.Errorf("resolve PostgreSQL migration advisory lock key: %w", err)
	}
	// Mark the lock as potentially held before the round trip. If the server
	// acquires it but the response is canceled/lost, cleanup will attempt an
	// unlock and discard the physical connection unless release is confirmed.
	guard.locked = true
	if _, err := session.ExecContext(ctx, `SELECT pg_advisory_lock($1, $2)`, guard.lockKeyOne, guard.lockKeyTwo); err != nil {
		return nil, fmt.Errorf("acquire PostgreSQL migration advisory lock: %w", err)
	}

	markerExists, err := postgresMigrationTableExists(ctx, session, guard.schema, "sqlite_postgres_migrations")
	if err != nil {
		return nil, err
	}
	if markerExists {
		markerTable := qualifiedMigrationTable(guard.schema, "sqlite_postgres_migrations")
		if err := session.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM `+markerTable+` WHERE migration_name=$1)`, sqlitePostgresMigrationName).Scan(&guard.completed); err != nil {
			return nil, fmt.Errorf("read SQLite migration marker: %w", err)
		}
	}
	if !guard.completed {
		if err := requireEmptyExistingMigrationTarget(ctx, session, guard.schema); err != nil {
			return nil, err
		}
	}

	cleanup = false
	return guard, nil
}

func postgresMigrationTableExists(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}, schema, table string) (bool, error) {
	var exists bool
	if err := queryer.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables WHERE table_schema=$1 AND table_name=$2
	)`, schema, table).Scan(&exists); err != nil {
		return false, fmt.Errorf("inspect PostgreSQL migration table %s: %w", table, err)
	}
	return exists, nil
}

func requireEmptyExistingMigrationTarget(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}, schema string) error {
	rows, err := queryer.QueryContext(ctx, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema=$1 AND table_name=ANY($2)
		ORDER BY table_name`, schema, pq.Array(sqlitePostgresBusinessTables))
	if err != nil {
		return fmt.Errorf("discover existing PostgreSQL migration target tables: %w", err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			return fmt.Errorf("scan existing PostgreSQL migration target table: %w", err)
		}
		tables = append(tables, table)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("discover existing PostgreSQL migration target tables: %w", err)
	}
	for _, table := range tables {
		var hasRows bool
		if err := queryer.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM `+qualifiedMigrationTable(schema, table)+` LIMIT 1)`).Scan(&hasRows); err != nil {
			return fmt.Errorf("inspect PostgreSQL migration target %s: %w", table, err)
		}
		if hasRows {
			return fmt.Errorf("PostgreSQL migration target is not empty: table=%s; automatic merge is refused", table)
		}
	}
	return nil
}

func (db *DB) autoMigrateSQLiteToPostgres(ctx context.Context, sourcePath string, guard *postgresAutoMigrationGuard) error {
	if db == nil || db.conn == nil {
		return errors.New("database is nil")
	}
	if db.isSQLite() {
		return errors.New("SQLite to PostgreSQL auto-migration requires the postgres driver")
	}
	validatedPath, err := validateSQLiteMigrationSource(sourcePath)
	if err != nil {
		return err
	}
	if guard == nil || guard.conn == nil || !guard.locked || guard.completed {
		return errors.New("PostgreSQL migration guard is not active")
	}
	schema := strings.TrimSpace(guard.schema)
	if schema == "" || len(schema) > 63 || !postgresSchemaNamePattern.MatchString(schema) {
		return fmt.Errorf("invalid PostgreSQL migration schema %q", schema)
	}

	targetTx, err := guard.conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin PostgreSQL migration transaction: %w", err)
	}
	defer targetTx.Rollback()
	if err := lockMigrationTargetTables(ctx, targetTx, schema); err != nil {
		return err
	}
	if err := requireEmptyMigrationTarget(ctx, targetTx, schema); err != nil {
		return err
	}

	source, err := openReadOnlyMigrationSQLite(validatedPath)
	if err != nil {
		return err
	}
	defer source.Close()
	if err := source.PingContext(ctx); err != nil {
		return fmt.Errorf("open SQLite migration source read-only: %w", err)
	}
	sourceTx, err := source.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin SQLite migration snapshot: %w", err)
	}
	defer sourceTx.Rollback()

	sourceTables, err := discoverMigrationSourceTables(ctx, sourceTx)
	if err != nil {
		return err
	}
	targetColumns, err := loadMigrationTargetColumns(ctx, targetTx, schema)
	if err != nil {
		return err
	}
	sourceCounts := make(map[string]int64, len(sqlitePostgresBusinessTables))
	var totalRows int64
	meaningfulSource := false
	for _, table := range sqlitePostgresBusinessTables {
		if _, ok := sourceTables[table]; !ok {
			continue
		}
		count, err := countMigrationSourceRows(ctx, sourceTx, table)
		if err != nil {
			return err
		}
		sourceCounts[table] = count
		totalRows += count
		if count > 0 && table != "usage_stats_baseline" {
			meaningfulSource = true
		}
	}
	if !meaningfulSource {
		meaningfulSource, err = migrationBaselineHasData(ctx, sourceTx, sourceTables)
		if err != nil {
			return err
		}
	}
	if !meaningfulSource {
		return errors.New("SQLite migration source contains no business data; refusing to mark migration complete")
	}

	for _, table := range sqlitePostgresBusinessTables {
		if _, ok := sourceTables[table]; !ok {
			continue
		}
		columns, ok := targetColumns[table]
		if !ok || len(columns) == 0 {
			return fmt.Errorf("PostgreSQL migration target table %s is missing", table)
		}
		copied, err := copyMigrationTable(ctx, sourceTx, targetTx, schema, table, columns)
		if err != nil {
			return err
		}
		if copied != sourceCounts[table] {
			return fmt.Errorf("migration row count changed while copying %s: source=%d copied=%d", table, sourceCounts[table], copied)
		}
		log.Printf("SQLite→PostgreSQL migration table=%s rows=%d", table, copied)
	}
	if err := verifyMigrationRowCounts(ctx, targetTx, schema, sourceCounts); err != nil {
		return err
	}
	if err := db.runDataMigrationsInTx(ctx, targetTx, true); err != nil {
		return err
	}
	if err := normalizePromptFilterNewAPIBindings(ctx, targetTx); err != nil {
		return fmt.Errorf("normalize imported NewAPI prompt bindings: %w", err)
	}
	if err := normalizePromptPolicyIncidentData(ctx, targetTx, true); err != nil {
		return fmt.Errorf("normalize imported prompt policy incidents: %w", err)
	}
	markerTable := qualifiedMigrationTable(schema, "sqlite_postgres_migrations")
	if _, err := targetTx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+markerTable+` (
		migration_name TEXT PRIMARY KEY,
		completed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		source_rows BIGINT NOT NULL DEFAULT 0
	)`); err != nil {
		return fmt.Errorf("create SQLite migration marker table: %w", err)
	}
	if _, err := targetTx.ExecContext(ctx, `INSERT INTO `+markerTable+` (migration_name, source_rows) VALUES ($1, $2)`, sqlitePostgresMigrationName, totalRows); err != nil {
		return fmt.Errorf("write SQLite migration marker: %w", err)
	}
	// PostgreSQL setval is not transactional. Keep sequence adjustment after
	// every foreseeable copy, semantic, verification, and marker failure. If a
	// later reset/commit outcome is uncertain, values can be left ahead (gaps)
	// but cannot be moved behind the imported maxima.
	if err := resetMigrationSequences(ctx, targetTx, schema, targetColumns); err != nil {
		return err
	}
	if err := targetTx.Commit(); err != nil {
		return fmt.Errorf("commit SQLite to PostgreSQL migration: %w", err)
	}
	log.Printf("SQLite→PostgreSQL migration complete tables=%d rows=%d", len(sourceCounts), totalRows)
	return nil
}

func resolveMigrationTargetSchema(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		if len(configured) > 63 || !postgresSchemaNamePattern.MatchString(configured) {
			return "", fmt.Errorf("invalid PostgreSQL migration schema %q", configured)
		}
		return configured, nil
	}
	var schema sql.NullString
	if err := queryer.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return "", fmt.Errorf("resolve PostgreSQL migration schema: %w", err)
	}
	if !schema.Valid || strings.TrimSpace(schema.String) == "" {
		return "", errors.New("PostgreSQL current_schema() is empty")
	}
	return schema.String, nil
}

func qualifiedMigrationTable(schema, table string) string {
	return pq.QuoteIdentifier(schema) + "." + pq.QuoteIdentifier(table)
}

func lockMigrationTargetTables(ctx context.Context, tx *sql.Tx, schema string) error {
	tables := make([]string, 0, len(sqlitePostgresBusinessTables))
	for _, table := range sqlitePostgresBusinessTables {
		tables = append(tables, qualifiedMigrationTable(schema, table))
	}
	if _, err := tx.ExecContext(ctx, "LOCK TABLE "+strings.Join(tables, ", ")+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		return fmt.Errorf("lock PostgreSQL migration target tables: %w", err)
	}
	return nil
}

func requireEmptyMigrationTarget(ctx context.Context, tx *sql.Tx, schema string) error {
	for _, table := range sqlitePostgresBusinessTables {
		var count int64
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualifiedMigrationTable(schema, table)).Scan(&count); err != nil {
			return fmt.Errorf("count PostgreSQL migration target %s: %w", table, err)
		}
		if count != 0 {
			return fmt.Errorf("PostgreSQL migration target is not empty: table=%s rows=%d; automatic merge is refused", table, count)
		}
	}
	return nil
}

func discoverMigrationSourceTables(ctx context.Context, tx *sql.Tx) (map[string]struct{}, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("discover SQLite migration tables: %w", err)
	}
	defer rows.Close()
	result := make(map[string]struct{})
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, fmt.Errorf("scan SQLite migration table: %w", err)
		}
		if _, ok := sqlitePostgresBusinessTableSet[table]; ok {
			result[table] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("discover SQLite migration tables: %w", err)
	}
	return result, nil
}

func countMigrationSourceRows(ctx context.Context, tx *sql.Tx, table string) (int64, error) {
	if _, ok := sqlitePostgresBusinessTableSet[table]; !ok {
		return 0, fmt.Errorf("SQLite migration table is not allowed: %q", table)
	}
	var count int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteSQLiteMigrationIdentifier(table)).Scan(&count); err != nil {
		return 0, fmt.Errorf("count SQLite migration source %s: %w", table, err)
	}
	return count, nil
}

func migrationBaselineHasData(ctx context.Context, tx *sql.Tx, sourceTables map[string]struct{}) (bool, error) {
	if _, ok := sourceTables["usage_stats_baseline"]; !ok {
		return false, nil
	}
	columns, err := loadMigrationSourceColumns(ctx, tx, "usage_stats_baseline")
	if err != nil {
		return false, err
	}
	allowed := map[string]struct{}{
		"total_requests": {}, "total_tokens": {}, "prompt_tokens": {}, "completion_tokens": {},
		"cached_tokens": {}, "cache_hit_requests": {}, "first_token_ms_sum": {}, "first_token_samples": {},
		"account_billed": {}, "user_billed": {},
	}
	expressions := make([]string, 0, len(allowed))
	for _, column := range columns {
		if _, ok := allowed[column.Name]; ok {
			expressions = append(expressions, "COALESCE("+quoteSQLiteMigrationIdentifier(column.Name)+", 0) <> 0")
		}
	}
	if len(expressions) == 0 {
		return false, nil
	}
	var hasData bool
	query := "SELECT EXISTS (SELECT 1 FROM \"usage_stats_baseline\" WHERE " + strings.Join(expressions, " OR ") + ")"
	if err := tx.QueryRowContext(ctx, query).Scan(&hasData); err != nil {
		return false, fmt.Errorf("inspect SQLite usage baseline: %w", err)
	}
	return hasData, nil
}

func loadMigrationTargetColumns(ctx context.Context, tx *sql.Tx, schema string) (map[string][]migrationTargetColumn, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT table_name, column_name, data_type, udt_name, column_default,
		       (is_identity='YES')
		FROM information_schema.columns
		WHERE table_schema=$1 AND table_name=ANY($2) AND is_generated='NEVER'
		ORDER BY table_name, ordinal_position`, schema, pq.Array(sqlitePostgresBusinessTables))
	if err != nil {
		return nil, fmt.Errorf("load PostgreSQL migration columns: %w", err)
	}
	defer rows.Close()
	result := make(map[string][]migrationTargetColumn, len(sqlitePostgresBusinessTables))
	for rows.Next() {
		var table string
		var column migrationTargetColumn
		if err := rows.Scan(&table, &column.Name, &column.DataType, &column.UDTName, &column.Default, &column.IsIdentity); err != nil {
			return nil, fmt.Errorf("scan PostgreSQL migration column: %w", err)
		}
		result[table] = append(result[table], column)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load PostgreSQL migration columns: %w", err)
	}
	for _, table := range sqlitePostgresBusinessTables {
		if len(result[table]) == 0 {
			return nil, fmt.Errorf("required PostgreSQL migration target table %s is missing", table)
		}
	}
	return result, nil
}

func loadMigrationSourceColumns(ctx context.Context, tx *sql.Tx, table string) ([]migrationSourceColumn, error) {
	if _, ok := sqlitePostgresBusinessTableSet[table]; !ok {
		return nil, fmt.Errorf("SQLite migration table is not allowed: %q", table)
	}
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+quoteSQLiteMigrationIdentifier(table)+")")
	if err != nil {
		return nil, fmt.Errorf("load SQLite migration columns for %s: %w", table, err)
	}
	defer rows.Close()
	columns := make([]migrationSourceColumn, 0)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue interface{}
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, fmt.Errorf("scan SQLite migration column for %s: %w", table, err)
		}
		columns = append(columns, migrationSourceColumn{Name: name, Type: dataType})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load SQLite migration columns for %s: %w", table, err)
	}
	return columns, nil
}

func copyMigrationTable(ctx context.Context, sourceTx, targetTx *sql.Tx, schema, table string, targetColumns []migrationTargetColumn) (int64, error) {
	sourceColumns, err := loadMigrationSourceColumns(ctx, sourceTx, table)
	if err != nil {
		return 0, err
	}
	intersection := intersectMigrationColumns(sourceColumns, targetColumns)
	if len(intersection) == 0 {
		count, countErr := countMigrationSourceRows(ctx, sourceTx, table)
		if countErr != nil {
			return 0, countErr
		}
		if count == 0 {
			return 0, nil
		}
		return 0, fmt.Errorf("SQLite migration table %s has rows but no compatible target columns", table)
	}

	quotedColumns := make([]string, len(intersection))
	identityOverride := false
	for i, column := range intersection {
		quotedColumns[i] = pq.QuoteIdentifier(column.Name)
		identityOverride = identityOverride || column.IsIdentity
	}
	sourceQuery := "SELECT " + strings.Join(quotedColumns, ", ") + " FROM " + quoteSQLiteMigrationIdentifier(table)
	sourceRows, err := sourceTx.QueryContext(ctx, sourceQuery)
	if err != nil {
		return 0, fmt.Errorf("read SQLite migration table %s: %w", table, err)
	}
	defer sourceRows.Close()

	insertPrefix := "INSERT INTO " + qualifiedMigrationTable(schema, table) + " (" + strings.Join(quotedColumns, ", ") + ")"
	if identityOverride {
		insertPrefix += " OVERRIDING SYSTEM VALUE"
	}

	values := make([]interface{}, len(intersection))
	scanTargets := make([]interface{}, len(intersection))
	batchLimit := 200
	if bindLimit := 60000 / len(intersection); bindLimit < batchLimit {
		batchLimit = bindLimit
	}
	if batchLimit < 1 {
		batchLimit = 1
	}
	batch := make([][]interface{}, 0, batchLimit)
	var copied int64
	for sourceRows.Next() {
		for i := range values {
			values[i] = nil
			scanTargets[i] = &values[i]
		}
		if err := sourceRows.Scan(scanTargets...); err != nil {
			return copied, fmt.Errorf("scan SQLite migration row from %s: %w", table, err)
		}
		for i, column := range intersection {
			values[i], err = convertMigrationValue(values[i], column)
			if err != nil {
				return copied, fmt.Errorf("convert SQLite migration value %s.%s: %w", table, column.Name, err)
			}
		}
		rowValues := append([]interface{}(nil), values...)
		batch = append(batch, rowValues)
		if len(batch) == batchLimit {
			if err := insertMigrationBatch(ctx, targetTx, insertPrefix, len(intersection), batch); err != nil {
				return copied, fmt.Errorf("insert PostgreSQL migration batch into %s: %w", table, err)
			}
			copied += int64(len(batch))
			batch = batch[:0]
		}
	}
	if err := sourceRows.Err(); err != nil {
		return copied, fmt.Errorf("read SQLite migration table %s: %w", table, err)
	}
	if len(batch) > 0 {
		if err := insertMigrationBatch(ctx, targetTx, insertPrefix, len(intersection), batch); err != nil {
			return copied, fmt.Errorf("insert PostgreSQL migration batch into %s: %w", table, err)
		}
		copied += int64(len(batch))
	}
	return copied, nil
}

func insertMigrationBatch(ctx context.Context, tx *sql.Tx, insertPrefix string, columnCount int, batch [][]interface{}) error {
	if len(batch) == 0 {
		return nil
	}
	valueGroups := make([]string, len(batch))
	args := make([]interface{}, 0, len(batch)*columnCount)
	bind := 1
	for rowIndex, row := range batch {
		if len(row) != columnCount {
			return fmt.Errorf("migration batch row has %d values, want %d", len(row), columnCount)
		}
		placeholders := make([]string, columnCount)
		for columnIndex := range row {
			placeholders[columnIndex] = fmt.Sprintf("$%d", bind)
			bind++
		}
		valueGroups[rowIndex] = "(" + strings.Join(placeholders, ", ") + ")"
		args = append(args, row...)
	}
	_, err := tx.ExecContext(ctx, insertPrefix+" VALUES "+strings.Join(valueGroups, ", "), args...)
	return err
}

func intersectMigrationColumns(sourceColumns []migrationSourceColumn, targetColumns []migrationTargetColumn) []migrationTargetColumn {
	sourceSet := make(map[string]struct{}, len(sourceColumns))
	for _, column := range sourceColumns {
		sourceSet[column.Name] = struct{}{}
	}
	intersection := make([]migrationTargetColumn, 0, len(targetColumns))
	for _, column := range targetColumns {
		if _, ok := sourceSet[column.Name]; ok {
			intersection = append(intersection, column)
		}
	}
	return intersection
}

func convertMigrationValue(value interface{}, column migrationTargetColumn) (interface{}, error) {
	if value == nil {
		return nil, nil
	}
	dataType := strings.ToLower(column.DataType)
	udtName := strings.ToLower(column.UDTName)
	switch {
	case dataType == "boolean" || udtName == "bool":
		return convertMigrationBool(value)
	case dataType == "json" || dataType == "jsonb" || udtName == "json" || udtName == "jsonb":
		return convertMigrationJSON(value)
	case strings.Contains(dataType, "timestamp") || dataType == "date" || udtName == "timestamp" || udtName == "timestamptz":
		return convertMigrationTime(value)
	case dataType == "smallint" || dataType == "integer" || dataType == "bigint" || udtName == "int2" || udtName == "int4" || udtName == "int8":
		return convertMigrationInteger(value)
	case dataType == "real" || dataType == "double precision" || dataType == "numeric" || dataType == "decimal" || udtName == "float4" || udtName == "float8" || udtName == "numeric":
		return convertMigrationNumber(value)
	case dataType == "bytea" || udtName == "bytea":
		switch typed := value.(type) {
		case []byte:
			return append([]byte(nil), typed...), nil
		case string:
			return []byte(typed), nil
		default:
			return nil, fmt.Errorf("unsupported bytea source type %T", value)
		}
	default:
		if bytes, ok := value.([]byte); ok {
			return string(bytes), nil
		}
		return value, nil
	}
}

func convertMigrationBool(value interface{}) (bool, error) {
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case int64:
		if typed == 0 || typed == 1 {
			return typed == 1, nil
		}
	case int:
		if typed == 0 || typed == 1 {
			return typed == 1, nil
		}
	case float64:
		if typed == 0 || typed == 1 {
			return typed == 1, nil
		}
	case []byte:
		return convertMigrationBool(string(typed))
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "0", "false", "f", "no", "off":
			return false, nil
		case "1", "true", "t", "yes", "on":
			return true, nil
		}
	}
	return false, fmt.Errorf("invalid SQLite boolean value %v (%T)", value, value)
}

func convertMigrationJSON(value interface{}) (string, error) {
	var raw []byte
	switch typed := value.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = typed
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("marshal JSON: %w", err)
		}
		raw = encoded
	}
	if !json.Valid(raw) {
		return "", errors.New("invalid JSON text")
	}
	return string(raw), nil
}

func convertMigrationTime(value interface{}) (time.Time, error) {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC(), nil
	case int64:
		return time.Unix(typed, 0).UTC(), nil
	case float64:
		seconds, fraction := math.Modf(typed)
		return time.Unix(int64(seconds), int64(fraction*float64(time.Second))).UTC(), nil
	case []byte:
		return parseMigrationTime(string(typed))
	case string:
		return parseMigrationTime(typed)
	default:
		return time.Time{}, fmt.Errorf("unsupported timestamp source type %T", value)
	}
}

func parseMigrationTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("empty timestamp")
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05 -0700 MST",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %q", value)
}

func convertMigrationInteger(value interface{}) (interface{}, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case bool:
		if typed {
			return int64(1), nil
		}
		return int64(0), nil
	case float64:
		if math.Trunc(typed) != typed || typed > math.MaxInt64 || typed < math.MinInt64 {
			return nil, fmt.Errorf("non-integral numeric value %v", typed)
		}
		return int64(typed), nil
	case []byte:
		return convertMigrationInteger(string(typed))
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return nil, err
		}
		return parsed, nil
	default:
		return nil, fmt.Errorf("unsupported integer source type %T", value)
	}
}

func convertMigrationNumber(value interface{}) (interface{}, error) {
	switch typed := value.(type) {
	case int64, int, float64:
		return typed, nil
	case bool:
		if typed {
			return int64(1), nil
		}
		return int64(0), nil
	case []byte:
		return convertMigrationNumber(string(typed))
	case string:
		trimmed := strings.TrimSpace(typed)
		if _, err := strconv.ParseFloat(trimmed, 64); err != nil {
			return nil, err
		}
		return trimmed, nil
	default:
		return nil, fmt.Errorf("unsupported numeric source type %T", value)
	}
}

func resetMigrationSequences(ctx context.Context, tx *sql.Tx, schema string, targetColumns map[string][]migrationTargetColumn) error {
	for _, table := range sqlitePostgresBusinessTables {
		for _, column := range targetColumns[table] {
			isSequence := column.IsIdentity || (column.Default.Valid && strings.HasPrefix(strings.ToLower(strings.TrimSpace(column.Default.String)), "nextval("))
			if !isSequence {
				continue
			}
			qualifiedTable := qualifiedMigrationTable(schema, table)
			var sequence sql.NullString
			if err := tx.QueryRowContext(ctx, `SELECT pg_get_serial_sequence($1, $2)`, qualifiedTable, column.Name).Scan(&sequence); err != nil {
				return fmt.Errorf("resolve PostgreSQL sequence for %s.%s: %w", table, column.Name, err)
			}
			if !sequence.Valid || strings.TrimSpace(sequence.String) == "" {
				continue
			}
			query := `SELECT setval($1::regclass, COALESCE(MAX(` + pq.QuoteIdentifier(column.Name) + `), 0) + 1, false) FROM ` + qualifiedTable
			if _, err := tx.ExecContext(ctx, query, sequence.String); err != nil {
				return fmt.Errorf("reset PostgreSQL sequence for %s.%s: %w", table, column.Name, err)
			}
		}
	}
	return nil
}

func verifyMigrationRowCounts(ctx context.Context, tx *sql.Tx, schema string, sourceCounts map[string]int64) error {
	for _, table := range sqlitePostgresBusinessTables {
		var targetCount int64
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualifiedMigrationTable(schema, table)).Scan(&targetCount); err != nil {
			return fmt.Errorf("verify PostgreSQL migration table %s: %w", table, err)
		}
		expected := sourceCounts[table]
		if targetCount != expected {
			return fmt.Errorf("migration row count mismatch for %s: source=%d target=%d", table, expected, targetCount)
		}
	}
	return nil
}

func quoteSQLiteMigrationIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
