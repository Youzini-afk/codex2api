package config

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// schemaNameRegex 限定 PostgreSQL schema 名为 ASCII 标识符，避免 DSN/DDL 注入。
var schemaNameRegex = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// IsValidSchemaName 校验 PostgreSQL schema 名（首字母为字母或下划线，余下为字母/数字/下划线，长度 ≤63）。
func IsValidSchemaName(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}
	return schemaNameRegex.MatchString(name)
}

// DatabaseConfig 数据库核心配置。
type DatabaseConfig struct {
	Driver           string
	ConnectionString string
	Path             string
	Host             string
	Port             int
	User             string
	Password         string
	DBName           string
	Schema           string // PostgreSQL schema（search_path）；空值保持数据库默认行为
	SSLMode          string
}

// DSN 返回当前驱动的连接字符串。
func (d *DatabaseConfig) DSN() string {
	if strings.EqualFold(d.Driver, "sqlite") {
		return d.Path
	}
	if strings.TrimSpace(d.ConnectionString) != "" {
		return d.ConnectionString
	}
	sslMode := d.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.DBName, sslMode)
	if d.Schema != "" {
		// 通过 libpq options 在连接启动时设置 search_path，覆盖连接池中的所有连接。
		// schema 已在 Load() 阶段做白名单校验，此处可安全拼接。
		dsn += fmt.Sprintf(" options='-c search_path=%s,public'", d.Schema)
	}
	return dsn
}

// Label 返回用于展示的数据库标签。
func (d *DatabaseConfig) Label() string {
	if strings.EqualFold(d.Driver, "sqlite") {
		return "SQLite"
	}
	return "PostgreSQL"
}

// RedisConfig Redis 核心配置
type RedisConfig struct {
	Addr               string
	Username           string
	Password           string
	DB                 int
	TLS                bool
	InsecureSkipVerify bool
	URL                string
}

// CacheConfig 缓存核心配置。
type CacheConfig struct {
	Driver string
	Redis  RedisConfig
}

// Label 返回用于展示的缓存标签。
func (c *CacheConfig) Label() string {
	if strings.EqualFold(c.Driver, "memory") {
		return "Memory"
	}
	return "Redis"
}

// Config 全局核心环境配置（物理隔离的服务器参数）
// 业务逻辑参数（如 ProxyURL，APIKeys，MaxConcurrency）已全部移至数据库 SystemSettings 进行化
type Config struct {
	Port                   int
	BindAddress            string // 监听地址，默认 0.0.0.0（兼容 Docker / 反代 / 公网）；如需仅本机访问可设为 127.0.0.1
	AdminSecret            string
	AllowAnonymousV1       bool // 显式允许 /v1/* 在未配置 API Key 时无鉴权放行（默认禁止）
	MaxRequestBodySize     int
	Database               DatabaseConfig
	Cache                  CacheConfig
	UseWebsocket           bool   // 是否启用 WebSocket 传输
	CodexUpstreamTransport string // http|auto|ws，默认 http；USE_WEBSOCKET 作为旧开关兼容
}

// Load 从 .env 文件加载核心环境配置，支持环境变量覆盖
func Load(envPath string) (*Config, error) {
	// 尝试加载 .env 文件（可选，如果文件不存在则忽略并使用当前环境变量）
	if envPath == "" {
		envPath = ".env"
	}
	_ = godotenv.Load(envPath)

	cfg := &Config{
		Port:               8080,
		MaxRequestBodySize: 32 * 1024 * 1024,
	}
	zeaburEnv := isZeaburEnvironment()

	// Web服务端口
	if port := os.Getenv("CODEX_PORT"); port != "" {
		fmt.Sscanf(port, "%d", &cfg.Port)
	} else if port := os.Getenv("PORT"); port != "" {
		fmt.Sscanf(port, "%d", &cfg.Port)
	}
	cfg.AdminSecret = strings.TrimSpace(os.Getenv("ADMIN_SECRET"))
	cfg.AllowAnonymousV1 = parseBoolEnv(os.Getenv("CODEX_ALLOW_ANONYMOUS"))
	// 默认绑 0.0.0.0 以兼容 Docker 端口映射、反向代理、生产服务器等常规部署。
	// 安全防护由 fail-closed 中间件 + 首启自助初始化 (/api/admin/bootstrap) + 启动 banner 共同保证；
	// 想要严格仅本机访问的用户可设 CODEX_BIND=127.0.0.1。
	cfg.BindAddress = strings.TrimSpace(os.Getenv("CODEX_BIND"))
	if cfg.BindAddress == "" {
		cfg.BindAddress = "0.0.0.0"
	}
	if v := strings.TrimSpace(os.Getenv("CODEX_MAX_REQUEST_BODY_SIZE_MB")); v != "" {
		if mb, err := strconv.Atoi(v); err == nil && mb > 0 {
			cfg.MaxRequestBodySize = mb * 1024 * 1024
		}
	}

	// Codex 上游传输配置。CODEX_UPSTREAM_TRANSPORT 优先；USE_WEBSOCKET 保留为旧开关。
	cfg.CodexUpstreamTransport = normalizeCodexUpstreamTransport(os.Getenv("CODEX_UPSTREAM_TRANSPORT"))
	if cfg.CodexUpstreamTransport == "" && parseBoolEnv(os.Getenv("USE_WEBSOCKET")) {
		cfg.CodexUpstreamTransport = "ws"
		cfg.UseWebsocket = true
	}
	if cfg.CodexUpstreamTransport == "" {
		cfg.CodexUpstreamTransport = "http"
	}
	if cfg.CodexUpstreamTransport == "ws" {
		cfg.UseWebsocket = true
	}

	dbDriverEnv := normalizeDriver(os.Getenv("DATABASE_DRIVER"), "")
	cacheDriverEnv := normalizeDriver(os.Getenv("CACHE_DRIVER"), "")

	// 数据库配置
	cfg.Database.ConnectionString = firstNonEmptyEnv(
		"DATABASE_URL",
		"POSTGRES_CONNECTION_STRING",
		"POSTGRESQL_CONNECTION_STRING",
		"POSTGRES_URL",
		"POSTGRESQL_URL",
	)
	if err := applyPostgresConnectionString(&cfg.Database, cfg.Database.ConnectionString); err != nil {
		return nil, fmt.Errorf("解析 PostgreSQL 连接字符串失败: %w", err)
	}
	cfg.Database.Path = strings.TrimSpace(firstNonEmptyEnv("DATABASE_PATH", "SQLITE_PATH"))
	cfg.Database.Host = firstNonEmpty(cfg.Database.Host, firstNonEmptyEnv("DATABASE_HOST", "POSTGRES_HOST", "POSTGRESQL_HOST"))
	if v := firstNonEmptyEnv("DATABASE_PORT", "POSTGRES_PORT", "POSTGRESQL_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Database.Port = p
		}
	}
	cfg.Database.User = firstNonEmpty(cfg.Database.User, firstNonEmptyEnv("DATABASE_USER", "POSTGRES_USER", "POSTGRES_USERNAME", "POSTGRESQL_USER", "POSTGRESQL_USERNAME"))
	cfg.Database.Password = firstNonEmpty(cfg.Database.Password, firstNonEmptyEnv("DATABASE_PASSWORD", "POSTGRES_PASSWORD", "POSTGRESQL_PASSWORD"))
	cfg.Database.DBName = firstNonEmpty(cfg.Database.DBName, firstNonEmptyEnv("DATABASE_NAME", "POSTGRES_DB", "POSTGRES_DATABASE", "POSTGRESQL_DB", "POSTGRESQL_DATABASE"))
	if v := strings.TrimSpace(os.Getenv("DATABASE_SCHEMA")); v != "" {
		if !IsValidSchemaName(v) {
			return nil, fmt.Errorf("非法的 DATABASE_SCHEMA: %q（仅允许字母、数字、下划线，且不能以数字开头，长度不超过 63）", v)
		}
		cfg.Database.Schema = v
	}
	if v := firstNonEmptyEnv("DATABASE_SSLMODE", "POSTGRES_SSLMODE", "POSTGRESQL_SSLMODE"); v != "" {
		cfg.Database.SSLMode = v
	}
	switch {
	case dbDriverEnv != "":
		cfg.Database.Driver = dbDriverEnv
	case cfg.Database.ConnectionString != "" || cfg.Database.Host != "":
		cfg.Database.Driver = "postgres"
	case zeaburEnv:
		cfg.Database.Driver = "sqlite"
	default:
		cfg.Database.Driver = "postgres"
	}
	if cfg.Database.Driver == "sqlite" && cfg.Database.Path == "" && zeaburEnv {
		cfg.Database.Path = "/data/codex2api.db"
	}

	// 缓存配置
	cfg.Cache.Redis.URL = firstNonEmptyEnv("REDIS_URL", "REDIS_CONNECTION_STRING", "CACHE_URL")
	if err := applyRedisConnectionString(&cfg.Cache.Redis, cfg.Cache.Redis.URL); err != nil {
		return nil, fmt.Errorf("解析 Redis 连接字符串失败: %w", err)
	}
	cfg.Cache.Redis.Addr = firstNonEmpty(cfg.Cache.Redis.Addr, firstNonEmptyEnv("REDIS_ADDR", "REDIS_HOST"))
	cfg.Cache.Redis.Username = firstNonEmpty(cfg.Cache.Redis.Username, firstNonEmptyEnv("REDIS_USERNAME"))
	cfg.Cache.Redis.Password = firstNonEmpty(cfg.Cache.Redis.Password, firstNonEmptyEnv("REDIS_PASSWORD"))
	if port := firstNonEmptyEnv("REDIS_PORT"); port != "" && cfg.Cache.Redis.Addr != "" && !strings.Contains(cfg.Cache.Redis.Addr, ":") {
		cfg.Cache.Redis.Addr = cfg.Cache.Redis.Addr + ":" + port
	}
	if v := firstNonEmptyEnv("REDIS_DB"); v != "" {
		if db, err := strconv.Atoi(v); err == nil {
			cfg.Cache.Redis.DB = db
		}
	}
	cfg.Cache.Redis.TLS = parseBoolEnv(os.Getenv("REDIS_TLS"))
	cfg.Cache.Redis.InsecureSkipVerify = parseBoolEnv(os.Getenv("REDIS_INSECURE_SKIP_VERIFY"))

	switch {
	case cacheDriverEnv != "":
		cfg.Cache.Driver = cacheDriverEnv
	case cfg.Cache.Redis.URL != "" || cfg.Cache.Redis.Addr != "":
		cfg.Cache.Driver = "redis"
	case zeaburEnv:
		cfg.Cache.Driver = "memory"
	default:
		cfg.Cache.Driver = "redis"
	}

	// 校验必填物理层配置
	switch cfg.Database.Driver {
	case "sqlite":
		if cfg.Database.Path == "" {
			return nil, fmt.Errorf("必须通过 .env 或环境变量配置 SQLite 数据库路径 (DATABASE_PATH)")
		}
	case "postgres":
		if cfg.Database.ConnectionString == "" && cfg.Database.Host == "" {
			return nil, fmt.Errorf("必须通过 .env 或环境变量配置 PostgreSQL (DATABASE_HOST)")
		}
	default:
		return nil, fmt.Errorf("不支持的数据库驱动: %s", cfg.Database.Driver)
	}
	if cfg.Database.Port == 0 {
		cfg.Database.Port = 5432
	}
	if cfg.Database.SSLMode == "" {
		cfg.Database.SSLMode = "disable"
	}

	switch cfg.Cache.Driver {
	case "memory":
	case "redis":
		if cfg.Cache.Redis.URL == "" && cfg.Cache.Redis.Addr == "" {
			return nil, fmt.Errorf("必须通过 .env 或环境变量配置 Redis (REDIS_ADDR)")
		}
	default:
		return nil, fmt.Errorf("不支持的缓存驱动: %s", cfg.Cache.Driver)
	}

	return cfg, nil
}

func normalizeDriver(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.ToLower(strings.TrimSpace(value))
}

func isZeaburEnvironment() bool {
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "ZEABUR_") {
			return true
		}
	}
	return false
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parseBoolEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func normalizeCodexUpstreamTransport(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "http", "https", "sse":
		return "http"
	case "auto":
		return "auto"
	case "ws", "websocket", "wss":
		return "ws"
	default:
		return ""
	}
}

func applyPostgresConnectionString(cfg *DatabaseConfig, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	cfg.ConnectionString = raw
	if !strings.Contains(raw, "://") {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	cfg.Host = firstNonEmpty(cfg.Host, parsed.Hostname())
	if port := parsed.Port(); port != "" && cfg.Port == 0 {
		if p, err := strconv.Atoi(port); err == nil {
			cfg.Port = p
		}
	}
	if parsed.User != nil {
		cfg.User = firstNonEmpty(cfg.User, parsed.User.Username())
		if password, ok := parsed.User.Password(); ok {
			cfg.Password = firstNonEmpty(cfg.Password, password)
		}
	}
	cfg.DBName = firstNonEmpty(cfg.DBName, strings.TrimPrefix(parsed.Path, "/"))
	cfg.SSLMode = firstNonEmpty(cfg.SSLMode, parsed.Query().Get("sslmode"))
	return nil
}

func applyRedisConnectionString(cfg *RedisConfig, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	cfg.URL = raw
	if !strings.Contains(raw, "://") {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	cfg.Addr = firstNonEmpty(cfg.Addr, parsed.Host)
	if parsed.User != nil {
		if password, ok := parsed.User.Password(); ok {
			cfg.Password = firstNonEmpty(cfg.Password, password)
		}
	}
	if dbPart := strings.Trim(parsed.Path, "/"); dbPart != "" {
		if db, err := strconv.Atoi(dbPart); err == nil {
			cfg.DB = db
		}
	}
	return nil
}
