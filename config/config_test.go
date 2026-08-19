package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultsToPostgresAndRedis(t *testing.T) {
	resetConfigEnv(t)

	// 不设置 DATABASE_DRIVER / CACHE_DRIVER，只提供各自默认驱动所需的最小参数。
	t.Setenv("DATABASE_HOST", "postgres")
	t.Setenv("REDIS_ADDR", "redis:6379")

	cfg, err := Load("__not_exists__.env")
	if err != nil {
		t.Fatalf("Load() 返回错误: %v", err)
	}

	if got := cfg.Database.Driver; got != "postgres" {
		t.Fatalf("Database.Driver = %q, want %q", got, "postgres")
	}
	if got := cfg.Cache.Driver; got != "redis" {
		t.Fatalf("Cache.Driver = %q, want %q", got, "redis")
	}
	if got := cfg.Database.Port; got != 5432 {
		t.Fatalf("Database.Port = %d, want %d", got, 5432)
	}
	if got := cfg.Database.SSLMode; got != "disable" {
		t.Fatalf("Database.SSLMode = %q, want %q", got, "disable")
	}
	if got := cfg.Port; got != 8080 {
		t.Fatalf("Port = %d, want %d", got, 8080)
	}
	if got := cfg.MaxRequestBodySize; got != 48*1024*1024 {
		t.Fatalf("MaxRequestBodySize = %d, want %d", got, 48*1024*1024)
	}
	if got := strings.Join(cfg.TrustedProxies, ","); got != "127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16" {
		t.Fatalf("TrustedProxies = %q, want loopback and private-network defaults", got)
	}
}

func TestLoadAllowsExplicitSQLiteAndMemory(t *testing.T) {
	resetConfigEnv(t)

	t.Setenv("DATABASE_DRIVER", "sqlite")
	t.Setenv("DATABASE_PATH", "/data/codex2api.db")
	t.Setenv("CACHE_DRIVER", "memory")

	cfg, err := Load("__not_exists__.env")
	if err != nil {
		t.Fatalf("Load() 返回错误: %v", err)
	}

	if got := cfg.Database.Driver; got != "sqlite" {
		t.Fatalf("Database.Driver = %q, want %q", got, "sqlite")
	}
	if got := cfg.Database.Path; got != "/data/codex2api.db" {
		t.Fatalf("Database.Path = %q, want %q", got, "/data/codex2api.db")
	}
	if got := cfg.Cache.Driver; got != "memory" {
		t.Fatalf("Cache.Driver = %q, want %q", got, "memory")
	}
}

func TestLoadReadsAdminSecretFromEnv(t *testing.T) {
	resetConfigEnv(t)

	t.Setenv("DATABASE_HOST", "postgres")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("ADMIN_SECRET", "from-env-secret")

	cfg, err := Load("__not_exists__.env")
	if err != nil {
		t.Fatalf("Load() 返回错误: %v", err)
	}

	if got := cfg.AdminSecret; got != "from-env-secret" {
		t.Fatalf("AdminSecret = %q, want %q", got, "from-env-secret")
	}
}

func TestLoadReadsMaxRequestBodySizeFromEnv(t *testing.T) {
	resetConfigEnv(t)

	t.Setenv("DATABASE_HOST", "postgres")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("CODEX_MAX_REQUEST_BODY_SIZE_MB", "64")

	cfg, err := Load("__not_exists__.env")
	if err != nil {
		t.Fatalf("Load() 返回错误: %v", err)
	}

	if got := cfg.MaxRequestBodySize; got != 64*1024*1024 {
		t.Fatalf("MaxRequestBodySize = %d, want %d", got, 64*1024*1024)
	}
}

func TestLoadParsesTrustedProxiesEnv(t *testing.T) {
	resetConfigEnv(t)
	t.Setenv("DATABASE_DRIVER", "")
	t.Setenv("DATABASE_HOST", "postgres")
	t.Setenv("CACHE_DRIVER", "")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("CODEX_TRUSTED_PROXIES", "10.0.0.0/8, 172.16.0.0/12;192.168.1.10")

	cfg, err := Load("__not_exists__.env")
	if err != nil {
		t.Fatalf("Load() 返回错误: %v", err)
	}
	if got := strings.Join(cfg.TrustedProxies, ","); got != "10.0.0.0/8,172.16.0.0/12,192.168.1.10" {
		t.Fatalf("TrustedProxies = %q", got)
	}
}

func TestLoadCanDisableTrustedProxies(t *testing.T) {
	resetConfigEnv(t)
	t.Setenv("DATABASE_DRIVER", "")
	t.Setenv("DATABASE_HOST", "postgres")
	t.Setenv("CACHE_DRIVER", "")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("CODEX_TRUSTED_PROXIES", "none")

	cfg, err := Load("__not_exists__.env")
	if err != nil {
		t.Fatalf("Load() 返回错误: %v", err)
	}
	if cfg.TrustedProxies != nil {
		t.Fatalf("TrustedProxies = %#v, want nil", cfg.TrustedProxies)
	}
}

func TestLoadDefaultsCodexUpstreamTransportToHTTP(t *testing.T) {
	resetConfigEnv(t)
	t.Setenv("DATABASE_DRIVER", "")
	t.Setenv("DATABASE_HOST", "postgres")
	t.Setenv("CACHE_DRIVER", "")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("CODEX_UPSTREAM_TRANSPORT", "")
	t.Setenv("USE_WEBSOCKET", "")

	cfg, err := Load("__not_exists__.env")
	if err != nil {
		t.Fatalf("Load() 返回错误: %v", err)
	}
	if got := cfg.CodexUpstreamTransport; got != "http" {
		t.Fatalf("CodexUpstreamTransport = %q, want http", got)
	}
}

func TestLoadUsesZeaburFallbacks(t *testing.T) {
	resetConfigEnv(t)

	t.Setenv("ZEABUR_PROJECT_ID", "project-123")
	t.Setenv("PORT", "5000")

	cfg, err := Load("__not_exists__.env")
	if err != nil {
		t.Fatalf("Load() 返回错误: %v", err)
	}

	if got := cfg.Port; got != 5000 {
		t.Fatalf("Port = %d, want %d", got, 5000)
	}
	if got := cfg.Database.Driver; got != "sqlite" {
		t.Fatalf("Database.Driver = %q, want %q", got, "sqlite")
	}
	if got := cfg.Database.Path; got != "/data/codex2api.db" {
		t.Fatalf("Database.Path = %q, want %q", got, "/data/codex2api.db")
	}
	if got := cfg.Cache.Driver; got != "memory" {
		t.Fatalf("Cache.Driver = %q, want %q", got, "memory")
	}
}

func TestLoadSupportsConnectionStringEnvs(t *testing.T) {
	resetConfigEnv(t)

	t.Setenv("DATABASE_URL", "postgresql://demo:secret@pg.internal:6543/codex?sslmode=require")
	t.Setenv("REDIS_URL", "redis://:redispass@redis.internal:6380/2")

	cfg, err := Load("__not_exists__.env")
	if err != nil {
		t.Fatalf("Load() 返回错误: %v", err)
	}

	if got := cfg.Database.Driver; got != "postgres" {
		t.Fatalf("Database.Driver = %q, want %q", got, "postgres")
	}
	if got := cfg.Database.DSN(); got != "postgresql://demo:secret@pg.internal:6543/codex?sslmode=require" {
		t.Fatalf("Database.DSN() = %q", got)
	}
	if got := cfg.Database.Host; got != "pg.internal" {
		t.Fatalf("Database.Host = %q, want %q", got, "pg.internal")
	}
	if got := cfg.Database.Port; got != 6543 {
		t.Fatalf("Database.Port = %d, want %d", got, 6543)
	}
	if got := cfg.Database.User; got != "demo" {
		t.Fatalf("Database.User = %q, want %q", got, "demo")
	}
	if got := cfg.Database.DBName; got != "codex" {
		t.Fatalf("Database.DBName = %q, want %q", got, "codex")
	}
	if got := cfg.Database.SSLMode; got != "require" {
		t.Fatalf("Database.SSLMode = %q, want %q", got, "require")
	}

	if got := cfg.Cache.Driver; got != "redis" {
		t.Fatalf("Cache.Driver = %q, want %q", got, "redis")
	}
	if got := cfg.Cache.Redis.URL; got != "redis://:redispass@redis.internal:6380/2" {
		t.Fatalf("Cache.Redis.URL = %q", got)
	}
	if got := cfg.Cache.Redis.Addr; got != "redis.internal:6380" {
		t.Fatalf("Cache.Redis.Addr = %q, want %q", got, "redis.internal:6380")
	}
	if got := cfg.Cache.Redis.Password; got != "redispass" {
		t.Fatalf("Cache.Redis.Password = %q, want %q", got, "redispass")
	}
	if got := cfg.Cache.Redis.DB; got != 2 {
		t.Fatalf("Cache.Redis.DB = %d, want %d", got, 2)
	}
}

func resetConfigEnv(t *testing.T) {
	t.Helper()

	keys := []string{
		"CODEX_PORT",
		"CODEX_MAX_REQUEST_BODY_SIZE_MB",
		"PORT",
		"ADMIN_SECRET",
		"DATABASE_DRIVER",
		"DATABASE_URL",
		"DATABASE_PATH",
		"DATABASE_AUTO_MIGRATE_FROM_SQLITE",
		"DATABASE_MIGRATION_SQLITE_PATH",
		"DATABASE_HOST",
		"DATABASE_PORT",
		"DATABASE_USER",
		"DATABASE_PASSWORD",
		"DATABASE_NAME",
		"DATABASE_SSLMODE",
		"DATABASE_SCHEMA",
		"POSTGRES_CONNECTION_STRING",
		"POSTGRESQL_CONNECTION_STRING",
		"POSTGRES_URL",
		"POSTGRESQL_URL",
		"POSTGRES_HOST",
		"POSTGRESQL_HOST",
		"POSTGRES_PORT",
		"POSTGRESQL_PORT",
		"POSTGRES_USER",
		"POSTGRES_USERNAME",
		"POSTGRESQL_USER",
		"POSTGRESQL_USERNAME",
		"POSTGRES_PASSWORD",
		"POSTGRESQL_PASSWORD",
		"POSTGRES_DB",
		"POSTGRES_DATABASE",
		"POSTGRESQL_DB",
		"POSTGRESQL_DATABASE",
		"POSTGRES_SSLMODE",
		"POSTGRESQL_SSLMODE",
		"CACHE_DRIVER",
		"REDIS_ADDR",
		"REDIS_HOST",
		"REDIS_PORT",
		"REDIS_PASSWORD",
		"REDIS_DB",
		"REDIS_URL",
		"REDIS_CONNECTION_STRING",
		"CACHE_URL",
		"ZEABUR_PROJECT_ID",
		"ZEABUR_SERVICE_NAME",
	}
	for _, key := range keys {
		t.Setenv(key, "")
	}
}

func TestLoadSQLiteAutoMigrationDefaultsOff(t *testing.T) {
	resetConfigEnv(t)
	t.Setenv("DATABASE_HOST", "postgres")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("DATABASE_MIGRATION_SQLITE_PATH", filepath.Join(t.TempDir(), "missing.sqlite"))

	cfg, err := Load("__not_exists__.env")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.AutoMigrateFromSQLite || cfg.Database.SQLiteMigrationSourcePath != "" {
		t.Fatalf("migration unexpectedly enabled: %#v", cfg.Database)
	}
}

func TestLoadSQLiteAutoMigrationPathPrecedence(t *testing.T) {
	resetConfigEnv(t)
	t.Setenv("DATABASE_HOST", "postgres")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("DATABASE_AUTO_MIGRATE_FROM_SQLITE", "true")
	explicit := filepath.Join(t.TempDir(), "explicit.sqlite")
	if err := os.WriteFile(explicit, []byte("placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_MIGRATION_SQLITE_PATH", explicit)
	t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "fallback.sqlite"))

	cfg, err := Load("__not_exists__.env")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Database.AutoMigrateFromSQLite || cfg.Database.SQLiteMigrationSourcePath != explicit {
		t.Fatalf("migration config = enabled:%v path:%q", cfg.Database.AutoMigrateFromSQLite, cfg.Database.SQLiteMigrationSourcePath)
	}
}

func TestLoadSQLiteAutoMigrationFallsBackToDatabasePath(t *testing.T) {
	resetConfigEnv(t)
	t.Setenv("DATABASE_HOST", "postgres")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("DATABASE_AUTO_MIGRATE_FROM_SQLITE", "1")
	fallback := filepath.Join(t.TempDir(), "database-path.sqlite")
	if err := os.WriteFile(fallback, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_PATH", fallback)

	cfg, err := Load("__not_exists__.env")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.SQLiteMigrationSourcePath != fallback {
		t.Fatalf("migration path=%q want=%q", cfg.Database.SQLiteMigrationSourcePath, fallback)
	}
}

func TestLoadSQLiteAutoMigrationRejectsInvalidConfiguration(t *testing.T) {
	t.Run("invalid boolean", func(t *testing.T) {
		resetConfigEnv(t)
		t.Setenv("DATABASE_HOST", "postgres")
		t.Setenv("REDIS_ADDR", "redis:6379")
		t.Setenv("DATABASE_AUTO_MIGRATE_FROM_SQLITE", "sometimes")
		if _, err := Load("__not_exists__.env"); err == nil || !strings.Contains(err.Error(), "true/false") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("missing source", func(t *testing.T) {
		resetConfigEnv(t)
		t.Setenv("DATABASE_HOST", "postgres")
		t.Setenv("REDIS_ADDR", "redis:6379")
		t.Setenv("DATABASE_AUTO_MIGRATE_FROM_SQLITE", "true")
		t.Setenv("DATABASE_MIGRATION_SQLITE_PATH", filepath.Join(t.TempDir(), "missing.sqlite"))
		if _, err := Load("__not_exists__.env"); err == nil || !strings.Contains(err.Error(), "源文件不可用") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("sqlite target", func(t *testing.T) {
		resetConfigEnv(t)
		t.Setenv("DATABASE_DRIVER", "sqlite")
		t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "source.sqlite"))
		t.Setenv("CACHE_DRIVER", "memory")
		t.Setenv("DATABASE_AUTO_MIGRATE_FROM_SQLITE", "true")
		if _, err := Load("__not_exists__.env"); err == nil || !strings.Contains(err.Error(), "DATABASE_DRIVER=postgres") {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestResolveSQLiteMigrationSourcePathZeaburDefault(t *testing.T) {
	if got := resolveSQLiteMigrationSourcePath("", "", true); got != "/data/codex2api.db" {
		t.Fatalf("Zeabur migration path=%q", got)
	}
	if got := resolveSQLiteMigrationSourcePath("/explicit.db", "/database.db", true); got != "/explicit.db" {
		t.Fatalf("explicit migration path=%q", got)
	}
}

func TestLoadAcceptsValidDatabaseSchema(t *testing.T) {
	t.Setenv("DATABASE_DRIVER", "")
	t.Setenv("DATABASE_HOST", "postgres")
	t.Setenv("DATABASE_NAME", "postgres")
	t.Setenv("DATABASE_SCHEMA", "codex2api")
	t.Setenv("CACHE_DRIVER", "")
	t.Setenv("REDIS_ADDR", "redis:6379")

	cfg, err := Load("__not_exists__.env")
	if err != nil {
		t.Fatalf("Load() 返回错误: %v", err)
	}
	if got := cfg.Database.Schema; got != "codex2api" {
		t.Fatalf("Database.Schema = %q, want codex2api", got)
	}
	dsn := cfg.Database.DSN()
	if !strings.Contains(dsn, "options='-c search_path=codex2api,public'") {
		t.Fatalf("DSN 未包含 search_path 选项: %s", dsn)
	}
}

func TestLoadRejectsInvalidDatabaseSchema(t *testing.T) {
	cases := []string{
		"public; DROP TABLE users",
		"with space",
		"1leading-digit",
		"with-dash",
		"中文",
		strings.Repeat("a", 64),
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("DATABASE_DRIVER", "")
			t.Setenv("DATABASE_HOST", "postgres")
			t.Setenv("DATABASE_SCHEMA", name)
			t.Setenv("CACHE_DRIVER", "")
			t.Setenv("REDIS_ADDR", "redis:6379")

			if _, err := Load("__not_exists__.env"); err == nil {
				t.Fatalf("非法 schema %q 应当被拒绝，但 Load() 通过了", name)
			}
		})
	}
}

func TestDSNOmitsSchemaWhenEmpty(t *testing.T) {
	d := DatabaseConfig{
		Driver:   "postgres",
		Host:     "h",
		Port:     5432,
		User:     "u",
		Password: "p",
		DBName:   "db",
		SSLMode:  "disable",
	}
	if got := d.DSN(); strings.Contains(got, "search_path") {
		t.Fatalf("空 schema 时 DSN 不应包含 search_path: %s", got)
	}
}

// issue #498: .env 里的 TZ 必须作用于 time.Local,否则自然日限额按宿主机时区重置。
func TestLoadAppliesTimezoneFromEnvFile(t *testing.T) {
	origLocal := time.Local
	t.Cleanup(func() { time.Local = origLocal })
	t.Setenv("TZ", "") // 注册测试结束后的恢复
	if err := os.Unsetenv("TZ"); err != nil {
		t.Fatalf("Unsetenv(TZ) 失败: %v", err)
	}
	t.Setenv("DATABASE_HOST", "postgres")
	t.Setenv("REDIS_ADDR", "redis:6379")

	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("TZ=America/Chicago\n"), 0o600); err != nil {
		t.Fatalf("写临时 .env 失败: %v", err)
	}

	if _, err := Load(envPath); err != nil {
		t.Fatalf("Load() 返回错误: %v", err)
	}
	if got := time.Local.String(); got != "America/Chicago" {
		t.Fatalf("time.Local = %q, want %q", got, "America/Chicago")
	}
}

func TestApplyTimezoneHandlesPosixColonPrefix(t *testing.T) {
	origLocal := time.Local
	t.Cleanup(func() { time.Local = origLocal })
	t.Setenv("TZ", ":Asia/Tokyo")

	applyTimezone()
	if got := time.Local.String(); got != "Asia/Tokyo" {
		t.Fatalf("time.Local = %q, want %q", got, "Asia/Tokyo")
	}
}

func TestApplyTimezoneKeepsLocalOnInvalidTZ(t *testing.T) {
	origLocal := time.Local
	t.Cleanup(func() { time.Local = origLocal })
	t.Setenv("TZ", "Not/A_Zone")

	applyTimezone()
	if time.Local != origLocal {
		t.Fatalf("非法 TZ 不应改动 time.Local, got %q", time.Local)
	}
}
