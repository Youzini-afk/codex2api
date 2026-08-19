# Codex2API 部署文档

本文档详细说明 Codex2API 的各种部署方式。

## 目录

- [部署模式概览](#部署模式概览)
- [快速开始](#快速开始)
- [Docker 部署](#docker-部署)
- [Render 镜像部署](#render-镜像部署)
- [本地开发](#本地开发)
- [生产环境配置](#生产环境配置)
- [升级指南](#升级指南)
- [备份与恢复](#备份与恢复)

---

## 部署模式概览

| 模式 | 适用场景 | 数据库 | 缓存 |
|------|----------|--------|------|
| **标准 Docker** | 生产环境推荐 | PostgreSQL | Redis |
| **SQLite 轻量** | 单机/测试环境 | SQLite | 内存 |
| **本地源码** | 开发调试 | 可选 | 可选 |

---

## 快速开始

### 1. 标准模式（推荐）

```bash
# 克隆仓库
git clone https://github.com/james-6-23/codex2api.git
cd codex2api

# 配置环境
cp .env.example .env
# 编辑 .env 文件，配置必要参数

# 启动服务
docker compose pull
docker compose up -d

# 查看日志
docker compose logs -f codex2api
```

### 2. SQLite 轻量模式

```bash
cp .env.sqlite.example .env
docker compose -f docker-compose.sqlite.yml pull
docker compose -f docker-compose.sqlite.yml up -d
```

### 3. Zeabur 自动部署

适用于 Zeabur 直接从 Git 仓库自动构建部署的场景。

**推荐做法：**

1. 使用仓库根目录的 `Dockerfile`
2. 在 Zeabur 中为服务挂载持久化目录到 `/data`
3. 至少设置 `ADMIN_SECRET`
4. 将健康检查路径指向 `/health`

**二改 fork 注意事项：**

- 推荐在 Zeabur 选择“从 Git 仓库部署”，仓库指向你的 fork，并使用根目录 `Dockerfile` 构建。
- 如果 Zeabur 服务使用“Docker Image”部署，镜像地址必须指向你的 fork 镜像，例如 `ghcr.io/<your-github-user>/codex2api:latest`；继续使用上游 `ghcr.io/james-6-23/codex2api:latest` 会看不到 fork 中的新功能。
- 本仓库的镜像构建工作流会在 `main` 分支推送后自动发布 `latest` 镜像，适合 Zeabur 镜像部署模式。

**默认行为：**

- 检测到 Zeabur 环境且未配置 PostgreSQL / Redis 时，服务会自动回退到 `SQLite + Memory`
- SQLite 默认写入 `/data/codex2api.db`
- Zeabur 自动注入的 `PORT` 会被自动识别，无需手动设置 `CODEX_PORT`

**如果接入 Zeabur 托管 PostgreSQL / Redis：**

可直接绑定以下变量：

```bash
DATABASE_URL=${POSTGRES_CONNECTION_STRING}
REDIS_URL=${REDIS_CONNECTION_STRING}
```

也兼容分拆变量，例如 `POSTGRESQL_HOST`、`POSTGRES_PORT`、`POSTGRESQL_USERNAME`、`POSTGRES_PASSWORD`、`POSTGRES_DATABASE`、`REDIS_HOST`、`REDIS_PORT` 等。

#### 从 Zeabur SQLite 一次性切换到 PostgreSQL + Redis

不要直接把数据库连接变量改成 PostgreSQL 后在线滚动发布。按以下停机流程执行：

1. 在 Zeabur **Suspend/停止旧 Codex2API 服务**，确保没有实例继续写 `/data/codex2api.db`。迁移读取 SQLite 的只读一致性快照，但实现无法对另一个进程取得排他锁；旧服务继续写入会造成快照之后的数据遗漏。
2. 停写后优先创建 Zeabur 持久卷快照，或在可访问持久卷的终端执行 `sqlite3 /data/codex2api.db ".backup '/data/codex2api.db.pre-postgres-backup'"`。如果只能文件冷备，必须确认所有 SQLite 进程已完全停止，并将 `codex2api.db` 与存在的 `codex2api.db-wal`、`codex2api.db-shm` 一致复制；WAL 模式下不得把单独 `cp` 主库当作安全备份。不要移动、改名或删除原文件。
3. 创建并绑定**全新的空 PostgreSQL** 与 Redis，同时继续把原持久卷挂载到 `/data`。Redis 只是缓存，Redis 切换与数据库迁移相互独立。
4. 首次启动只保留一个应用实例，并临时配置：

   ```env
   DATABASE_DRIVER=postgres
   DATABASE_URL=${POSTGRES_CONNECTION_STRING}
   CACHE_DRIVER=redis
   REDIS_URL=${REDIS_CONNECTION_STRING}
   DATABASE_AUTO_MIGRATE_FROM_SQLITE=true
   DATABASE_MIGRATION_SQLITE_PATH=/data/codex2api.db
   ```

5. 启动并查看日志。首次必须继续保持单实例：服务在目标空库预检通过后初始化 PostgreSQL schema，再在任何 usage log、prompt audit 或其他后台 writer 启动前迁移。日志只输出表名和行数；看到完成日志后，核验账号/API Key 数量、系统设置、历史用量、prompt/risk 数据和生图记录。PostgreSQL advisory lock 仅作为多个自动迁移 worker 被误启动时的附加串行保护，不代替 Suspend、停写和单实例流程。原始复制、校验、数据回填和完成 marker 位于同一导入事务；所有可预见的数据、语义与 marker 失败都发生在最后的序列校正之前。PostgreSQL `setval` 不随事务回滚，极少数序列校正或提交结果不确定的错误可能留下无害的 ID 空洞，但不会提交部分业务数据或完成 marker，修复后可重试。
6. 核验成功后把 `DATABASE_AUTO_MIGRATE_FROM_SQLITE` 设为 `false`（或删除），再正常部署/扩容。完成 marker 会让重复首次启动幂等跳过，但迁移开关不应长期保留。
7. 长期保留 SQLite 备份。`/data/images`、`/data/backgrounds` 实体文件不会被搬运或删除，只有图片 metadata/设置进入 PostgreSQL；因此成功后也必须继续挂载原 `/data`。

源文件不存在、源库没有真实业务数据（只有全零默认 baseline），或目标 PostgreSQL 任一业务表已有数据时，应用会拒绝启动；它不会把错误路径当成功，也不会自动 merge/覆盖目标数据。

**示例文件：**

- 根目录 `.env.zeabur.example`

---

## Docker 部署

### 标准模式（PostgreSQL + Redis）

**docker-compose.yml 服务组成:**

```yaml
services:
  codex2api:    # 主应用服务
  postgres:     # PostgreSQL 数据库
  redis:        # Redis 缓存
```

**数据持久化:**

| 卷名 | 用途 |
|------|------|
| codex2api_pgdata | PostgreSQL 数据 |
| codex2api_redisdata | Redis 数据 |

**完整部署流程:**

```bash
# 1. 准备环境文件
cp .env.example .env

# 2. 修改 .env 配置
# - CODEX_PORT: 服务端口
# - ADMIN_SECRET: 管理后台密码
# - DATABASE_*: 数据库配置
# - REDIS_*: Redis 配置

# 3. 启动服务
docker compose pull
docker compose up -d

# 4. 验证状态
docker compose ps
docker compose logs -f codex2api

# 5. 访问服务
# 管理后台: http://localhost:8080/admin/
# API 地址: http://localhost:8080/v1/
```

### SQLite 轻量模式

**docker-compose.sqlite.yml 服务组成:**

```yaml
services:
  codex2api:    # 主应用服务（单容器）
```

**数据持久化:**

| 卷名 | 用途 |
|------|------|
| codex2api-sqlite_sqlite-data | SQLite 数据库文件 |

**部署流程:**

```bash
# 1. 准备环境文件
cp .env.sqlite.example .env

# 2. 修改 .env 配置
# - CODEX_PORT: 服务端口
# - DATABASE_PATH: /data/codex2api.db

# 3. 启动服务
docker compose -f docker-compose.sqlite.yml pull
docker compose -f docker-compose.sqlite.yml up -d
```

### 本地源码构建模式

用于本地修改代码后验证:

```bash
# 标准模式本地构建
docker compose -f docker-compose.local.yml up -d --build

# SQLite 模式本地构建
docker compose -f docker-compose.sqlite.local.yml up -d --build
```

**注意:** 本地构建模式使用 `build: .` 而非预构建镜像。

---

## Render 镜像部署

Render 可以直接运行 GHCR 中已经构建好的镜像，适合放一个公开 Demo 或轻量测试环境。

当前公开 Demo：

| 项目 | 地址 |
|------|------|
| Demo 首页 | [https://codex2api-latest-vu8j.onrender.com](https://codex2api-latest-vu8j.onrender.com) |
| Demo 密码 | `codex2api` |

> Demo 环境仅用于体验管理后台界面和基础功能，请勿上传真实 Refresh Token、Access Token、API Key 或其他敏感信息。

### 1. 创建 Image-backed Web Service

在 Render Dashboard 中创建 `Web Service`，选择 `Existing Image`，镜像地址填写：

```text
ghcr.io/james-6-23/codex2api:latest
```

如果 GHCR Package 不是公开访问，需要先在 Render 的 Registry Credentials 中配置 GitHub Container Registry 凭据。

### 2. 配置环境变量

免费实例没有持久化磁盘，推荐 Demo 使用 SQLite + 内存缓存，并把可写目录放到 `/tmp`：

```env
CODEX_PORT=10000
CODEX_BIND=0.0.0.0
DATABASE_DRIVER=sqlite
DATABASE_PATH=/tmp/codex2api.db
CACHE_DRIVER=memory
IMAGE_ASSET_DIR=/tmp/images
LOG_DIR=/tmp/logs
LOG_DISABLED=true
ADMIN_SECRET=replace-with-a-strong-secret
CODEX_ALLOW_ANONYMOUS=false
GIN_MODE=release
TZ=Asia/Shanghai
```

Render Web Service 默认会通过 `PORT=10000` 暴露服务；本项目也会读取 `PORT`，但显式设置 `CODEX_PORT=10000` 更直观。

### 3. 配置自动部署最新镜像

Render 的 image-backed 服务不会在 `latest` 标签更新后自动重新部署。需要使用服务 Settings 中的 Deploy Hook：

1. 打开 Render 服务的 `Settings`。
2. 找到 `Deploy Hook` 并复制 URL。
3. 到 GitHub 仓库 `Settings` → `Secrets and variables` → `Actions`。
4. 新增仓库 Secret：

```text
RENDER_DEPLOY_HOOK_URL=<Render Deploy Hook URL>
```

之后 `.github/workflows/render-deploy.yml` 会在 `Build Docker Image` 工作流成功后自动请求该 Hook，让 Render 重新拉取 `ghcr.io/james-6-23/codex2api:latest` 并部署。

也可以手动触发镜像构建工作流，镜像推送成功后会自动进入 Render 部署工作流：

```bash
gh workflow run docker-image.yml
```

如果只想让 Render 重新拉取当前 `latest` 镜像，也可以单独触发 Render 部署工作流：

```bash
gh workflow run render-deploy.yml
```

### 4. 验证

部署完成后访问：

```text
https://<your-service>.onrender.com/admin/
https://<your-service>.onrender.com/health
```

注意：Render 免费实例适合 Demo，不建议承载真实 Token 或生产流量；实例休眠、重启或重新部署后，`/tmp` 中的 SQLite 数据和图库可能丢失。

---

## 本地开发

### 环境要求

- Go 1.26.6+
- Node.js 22.12+
- PostgreSQL 14+ (可选，可用 SQLite)
- Redis 7+ (可选，可用内存缓存)

### 后端开发

```bash
# 1. 安装依赖
go mod download

# 2. 配置环境
cp .env.example .env
# 编辑 .env 配置本地数据库

# 3. 构建前端（必须，因为 Go 使用 go:embed 嵌入）
cd frontend && npm ci && npm run build && cd ..

# 4. 启动后端
go run .
```

### 前端开发

```bash
cd frontend

# 安装依赖
npm ci

# 修改前端后运行验证
npm test
npm run typecheck

# 启动开发服务器
npm run dev
```

Vite 配置已包含代理规则，开发时访问 `http://localhost:5173/admin/`，API 请求会自动代理到后端。

**vite.config.js 代理配置:**

```javascript
server: {
  proxy: {
    '/api': 'http://localhost:8080',
    '/health': 'http://localhost:8080',
    '/v1': 'http://localhost:8080',
  }
}
```

---

## 生产环境配置

### 1. 环境变量配置

**必需配置:**

```bash
# 服务端口
CODEX_PORT=8080

# 管理后台密码（强密码推荐）
ADMIN_SECRET=your-strong-password-here

# 数据库配置（PostgreSQL 模式）
DATABASE_DRIVER=postgres
DATABASE_HOST=postgres
DATABASE_PORT=5432
DATABASE_USER=codex2api
DATABASE_PASSWORD=your-db-password
DATABASE_NAME=codex2api

# Redis 配置
CACHE_DRIVER=redis
REDIS_ADDR=redis:6379
REDIS_USERNAME=
REDIS_PASSWORD=your-redis-password
REDIS_DB=0
REDIS_TLS=false
REDIS_INSECURE_SKIP_VERIFY=false

# 时区
TZ=Asia/Shanghai
```

云 Redis（如 Aiven、Upstash）通常需要 TLS，可直接使用平台提供的 `rediss://` 连接串：

```env
CACHE_DRIVER=redis
REDIS_ADDR=rediss://default:your-redis-password@your-redis-host:6379/0
```

**可选配置:**

```bash
# 快速调度器
FAST_SCHEDULER_ENABLED=true
```

### 2. 系统设置（通过管理后台）

首次启动后访问 `/admin/settings` 配置:

| 参数 | 建议值 | 说明 |
|------|--------|------|
| Max Concurrency | 2-4 | 单账号最大并发 |
| Global RPM | 0 或 1000+ | 0 表示不限流 |
| Test Model | gpt-5.4 | 测试连接用模型 |
| Test Concurrency | 50 | 批量测试并发数 |
| PgMax Conns | 50 | PostgreSQL 连接池 |
| Redis Pool Size | 30 | Redis 连接池 |

### 3. 反向代理配置

**Nginx 配置示例:**

```nginx
server {
    listen 80;
    server_name codex.example.com;

    # 强制 HTTPS（生产环境推荐）
    return 301 https://$server_name$request_uri;
}

server {
    listen 443 ssl http2;
    server_name codex.example.com;

    ssl_certificate /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;

    # 管理后台（可添加额外认证）
    location /admin/ {
        proxy_pass http://localhost:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }

    # API 端点
    location /v1/ {
        proxy_pass http://localhost:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;

        # 流式响应优化
        proxy_buffering off;
        proxy_cache off;
    }

    # 健康检查
    location /health {
        proxy_pass http://localhost:8080;
    }
}
```

### 4. Docker Compose 生产配置

```yaml
version: '3.8'

services:
  codex2api:
    image: ghcr.io/james-6-23/codex2api:latest
    container_name: codex2api
    restart: unless-stopped
    env_file:
      - .env
    ports:
      - "127.0.0.1:8080:8080"  # 仅本地监听，通过 nginx 暴露
    depends_on:
      postgres:
        condition: service_healthy
      redis:
        condition: service_healthy
    networks:
      - codex2api
    logging:
      driver: "json-file"
      options:
        max-size: "100m"
        max-file: "3"

  postgres:
    image: postgres:15-alpine
    container_name: codex2api-postgres
    restart: unless-stopped
    environment:
      POSTGRES_USER: ${DATABASE_USER}
      POSTGRES_PASSWORD: ${DATABASE_PASSWORD}
      POSTGRES_DB: ${DATABASE_NAME}
    volumes:
      - pgdata:/var/lib/postgresql/data
    networks:
      - codex2api
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${DATABASE_USER} -d ${DATABASE_NAME}"]
      interval: 5s
      timeout: 5s
      retries: 5

  redis:
    image: redis:7-alpine
    container_name: codex2api-redis
    restart: unless-stopped
    command: redis-server --requirepass ${REDIS_PASSWORD}
    volumes:
      - redisdata:/data
    networks:
      - codex2api
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 5s
      retries: 5

volumes:
  pgdata:
  redisdata:

networks:
  codex2api:
    driver: bridge
```

---

## 升级指南

### 标准升级流程

```bash
# 1. 备份数据库（重要！）
docker exec codex2api-postgres pg_dump -U codex2api codex2api > backup_$(date +%Y%m%d_%H%M%S).sql

# 2. 拉取新版本
git pull
docker compose pull

# 3. 滚动更新（零停机）
docker compose up -d

# 4. 验证状态
docker compose ps
docker compose logs -f codex2api

# 5. 健康检查
curl http://localhost:8080/health
```

### 版本降级

```bash
# 1. 停止服务
docker compose down

# 2. 恢复数据库
docker exec -i codex2api-postgres psql -U codex2api codex2api < backup_xxx.sql

# 3. 指定旧版本启动
# 编辑 docker-compose.yml，指定 image:tag
docker compose up -d
```

### SQLite 模式升级

```bash
# 先停止所有写入实例，再生成 SQLite 一致备份
docker compose -f docker-compose.sqlite.yml stop
sqlite3 /path/to/codex2api.db ".backup '/path/to/codex2api.db.backup_$(date +%Y%m%d_%H%M%S)'"

# 升级
docker compose -f docker-compose.sqlite.yml pull
docker compose -f docker-compose.sqlite.yml up -d
```

---

## 备份与恢复

### PostgreSQL 备份

**自动备份脚本:**

```bash
#!/bin/bash
# backup.sh

BACKUP_DIR="/backup/codex2api"
DATE=$(date +%Y%m%d_%H%M%S)
CONTAINER="codex2api-postgres"
DB_NAME="codex2api"
DB_USER="codex2api"

# 创建备份目录
mkdir -p $BACKUP_DIR

# 执行备份
docker exec $CONTAINER pg_dump -U $DB_USER $DB_NAME > $BACKUP_DIR/codex2api_$DATE.sql

# 保留最近 30 天备份
find $BACKUP_DIR -name "*.sql" -mtime +30 -delete

echo "Backup completed: $BACKUP_DIR/codex2api_$DATE.sql"
```

**添加到定时任务:**

```bash
# 每天凌晨 2 点执行备份
0 2 * * * /path/to/backup.sh >> /var/log/codex2api-backup.log 2>&1
```

### PostgreSQL 恢复

```bash
# 1. 停止应用
docker compose stop codex2api

# 2. 恢复数据库
docker exec -i codex2api-postgres psql -U codex2api -d codex2api < backup_xxx.sql

# 3. 重启服务
docker compose start codex2api
```

### SQLite 备份

```bash
# 首选：SQLite 一致备份
sqlite3 /data/codex2api.db ".backup '/backup/codex2api_$(date +%Y%m%d_%H%M%S).db'"
```

如果只能使用文件级冷备，必须先完全停止所有读写 SQLite 的进程，再把主库与存在的 `-wal` / `-shm` 文件作为一组一致复制。不要在 WAL 模式下单独复制主库文件。

### SQLite 恢复

```bash
# 停止服务
docker compose -f docker-compose.sqlite.yml stop

# 恢复数据
cp /backup/codex2api_xxx.db /data/codex2api.db

# 启动服务
docker compose -f docker-compose.sqlite.yml start
```

---

## 容器名与卷名对照

| 部署模式 | 容器名 | 数据卷 |
|----------|--------|--------|
| 标准镜像 | codex2api | codex2api_pgdata, codex2api_redisdata |
| 标准本地 | codex2api-local | codex2api-local_pgdata, codex2api-local_redisdata |
| SQLite 镜像 | codex2api-sqlite | codex2api-sqlite_sqlite-data |
| SQLite 本地 | codex2api-sqlite-local | codex2api-sqlite-local_sqlite-data-local |

**注意:** 不同模式的数据卷相互隔离，切换 compose 文件后看到空数据是正常现象。
