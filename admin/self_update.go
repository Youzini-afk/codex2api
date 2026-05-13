package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	defaultDockerSocketPath = "/var/run/docker.sock"
	watchtowerImage         = "containrrr/watchtower:latest"
	selfUpdateTimeout       = 2 * time.Minute
	selfUpdateRunWindow     = 10 * time.Minute
)

type selfUpdateStatusResponse struct {
	Supported       bool   `json:"supported"`
	Running         bool   `json:"running"`
	Method          string `json:"method,omitempty"`
	TargetContainer string `json:"target_container,omitempty"`
	TargetImage     string `json:"target_image,omitempty"`
	WatchtowerImage string `json:"watchtower_image,omitempty"`
	Message         string `json:"message,omitempty"`
	Error           string `json:"error,omitempty"`
	Reason          string `json:"reason,omitempty"`
	StartedAt       string `json:"started_at,omitempty"`
}

type selfUpdateStartReq struct {
	Version string `json:"version"`
}

type dockerInspectResponse struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Image string `json:"Image"`
	} `json:"Config"`
}

type dockerCreateResponse struct {
	ID       string   `json:"Id"`
	Warnings []string `json:"Warnings"`
}

type selfUpdateTarget struct {
	Container string
	Image     string
}

func dockerSocketPath() string {
	if value := strings.TrimSpace(os.Getenv("CODEX2API_DOCKER_SOCKET")); value != "" {
		return value
	}
	return defaultDockerSocketPath
}

func newDockerSocketClient(socketPath string) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}
}

func dockerRequest(ctx context.Context, client *http.Client, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return client.Do(req)
}

func dockerReadError(resp *http.Response) string {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if len(body) == 0 {
		return resp.Status
	}
	return strings.TrimSpace(string(body))
}

func dockerPing(ctx context.Context, client *http.Client) error {
	resp, err := dockerRequest(ctx, client, http.MethodGet, "/_ping", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Docker API ping failed: %s", resp.Status)
	}
	return nil
}

func dockerInspectContainer(ctx context.Context, client *http.Client, idOrName string) (*dockerInspectResponse, error) {
	path := "/containers/" + url.PathEscape(strings.TrimSpace(idOrName)) + "/json"
	resp, err := dockerRequest(ctx, client, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.New(dockerReadError(resp))
	}
	defer resp.Body.Close()

	var inspected dockerInspectResponse
	if err := json.NewDecoder(resp.Body).Decode(&inspected); err != nil {
		return nil, err
	}
	return &inspected, nil
}

func resolveSelfUpdateTarget(ctx context.Context, client *http.Client) (*selfUpdateTarget, error) {
	candidates := make([]string, 0, 4)
	if value := strings.TrimSpace(os.Getenv("CODEX2API_SELF_UPDATE_CONTAINER")); value != "" {
		candidates = append(candidates, value)
	}
	if hostname, err := os.Hostname(); err == nil && strings.TrimSpace(hostname) != "" {
		candidates = append(candidates, strings.TrimSpace(hostname))
	}
	candidates = append(candidates, "codex2api", "codex2api-sqlite")

	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true

		inspected, err := dockerInspectContainer(ctx, client, candidate)
		if err != nil {
			continue
		}
		name := strings.TrimPrefix(inspected.Name, "/")
		if name == "" {
			name = candidate
		}
		return &selfUpdateTarget{
			Container: name,
			Image:     inspected.Config.Image,
		}, nil
	}

	return nil, fmt.Errorf("未找到当前 codex2api 容器，请设置 CODEX2API_SELF_UPDATE_CONTAINER")
}

func getSelfUpdateCapability(ctx context.Context) (*selfUpdateTarget, string) {
	socketPath := dockerSocketPath()
	if stat, err := os.Stat(socketPath); err != nil {
		return nil, "未检测到 Docker socket，请在 docker-compose.yml 的 codex2api 服务 volumes 中配置 - /var/run/docker.sock:/var/run/docker.sock 后重启容器"
	} else if stat.IsDir() {
		return nil, "Docker socket 路径不是 socket 文件"
	}

	client := newDockerSocketClient(socketPath)
	if err := dockerPing(ctx, client); err != nil {
		return nil, "无法连接 Docker API: " + err.Error()
	}

	target, err := resolveSelfUpdateTarget(ctx, client)
	if err != nil {
		return nil, err.Error()
	}
	return target, ""
}

// GetSelfUpdateStatus 返回一键更新能力检测结果。
func (h *Handler) GetSelfUpdateStatus(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	target, reason := getSelfUpdateCapability(ctx)

	h.selfUpdateMu.Lock()
	running := h.selfUpdateRunning && time.Since(h.selfUpdateStartedAt) < selfUpdateRunWindow
	if !running && h.selfUpdateRunning {
		h.selfUpdateRunning = false
	}
	resp := selfUpdateStatusResponse{
		Supported:       target != nil,
		Running:         running,
		Method:          "watchtower",
		WatchtowerImage: watchtowerImage,
		Message:         h.selfUpdateMessage,
		Error:           h.selfUpdateError,
	}
	if !h.selfUpdateStartedAt.IsZero() {
		resp.StartedAt = h.selfUpdateStartedAt.Format(time.RFC3339)
	}
	h.selfUpdateMu.Unlock()

	if target != nil {
		resp.TargetContainer = target.Container
		resp.TargetImage = target.Image
	} else {
		resp.Reason = reason
	}
	c.JSON(http.StatusOK, resp)
}

// StartSelfUpdate 启动一次性 Watchtower 容器，由它拉取最新镜像并重建当前容器。
func (h *Handler) StartSelfUpdate(c *gin.Context) {
	var req selfUpdateStartReq
	_ = c.ShouldBindJSON(&req)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	target, reason := getSelfUpdateCapability(ctx)
	if target == nil {
		c.JSON(http.StatusPreconditionFailed, gin.H{
			"error":     reason,
			"supported": false,
		})
		return
	}

	h.selfUpdateMu.Lock()
	if h.selfUpdateRunning && time.Since(h.selfUpdateStartedAt) < selfUpdateRunWindow {
		h.selfUpdateMu.Unlock()
		c.JSON(http.StatusConflict, gin.H{
			"error": "已有更新任务正在执行",
		})
		return
	}
	h.selfUpdateRunning = true
	h.selfUpdateStartedAt = time.Now()
	h.selfUpdateMessage = "更新任务已启动，正在准备 Watchtower"
	h.selfUpdateError = ""
	h.selfUpdateMu.Unlock()

	go h.runWatchtowerSelfUpdate(target, strings.TrimSpace(req.Version))

	c.JSON(http.StatusAccepted, gin.H{
		"message":          "更新任务已启动，容器将拉取最新镜像并自动重启",
		"supported":        true,
		"target_container": target.Container,
		"target_image":     target.Image,
		"watchtower_image": watchtowerImage,
	})
}

func (h *Handler) setSelfUpdateState(running bool, message, errorMessage string) {
	h.selfUpdateMu.Lock()
	defer h.selfUpdateMu.Unlock()
	h.selfUpdateRunning = running
	if message != "" {
		h.selfUpdateMessage = message
	}
	h.selfUpdateError = errorMessage
}

func (h *Handler) runWatchtowerSelfUpdate(target *selfUpdateTarget, version string) {
	ctx, cancel := context.WithTimeout(context.Background(), selfUpdateTimeout)
	defer cancel()

	socketPath := dockerSocketPath()
	client := newDockerSocketClient(socketPath)

	h.setSelfUpdateState(true, "正在拉取 Watchtower 更新器镜像", "")
	if err := dockerPullImage(ctx, client, watchtowerImage); err != nil {
		h.setSelfUpdateState(false, "", "拉取 Watchtower 镜像失败: "+err.Error())
		return
	}

	containerName := "codex2api-updater-" + strconvFormatUnix(time.Now())
	cmd := []string{"--run-once", "--cleanup", "--rolling-restart", target.Container}
	body := map[string]any{
		"Image": watchtowerImage,
		"Cmd":   cmd,
		"Env": []string{
			"WATCHTOWER_NO_STARTUP_MESSAGE=true",
		},
		"Labels": map[string]string{
			"com.codex2api.self-update":       "true",
			"com.codex2api.target":            target.Container,
			"com.codex2api.requested-version": version,
		},
		"HostConfig": map[string]any{
			"Binds":      []string{socketPath + ":/var/run/docker.sock"},
			"AutoRemove": true,
		},
	}

	h.setSelfUpdateState(true, "正在创建一次性更新器容器", "")
	createResp, err := dockerCreateContainer(ctx, client, containerName, body)
	if err != nil {
		h.setSelfUpdateState(false, "", "创建更新器容器失败: "+err.Error())
		return
	}

	h.setSelfUpdateState(true, "正在启动更新器容器，服务稍后会自动重启", "")
	if err := dockerStartContainer(ctx, client, createResp.ID); err != nil {
		h.setSelfUpdateState(false, "", "启动更新器容器失败: "+err.Error())
		return
	}

	h.setSelfUpdateState(true, "更新器已启动，正在后台拉取最新镜像并重启服务", "")
}

func dockerPullImage(ctx context.Context, client *http.Client, image string) error {
	fromImage, tag := splitDockerImage(image)
	path := "/images/create?fromImage=" + url.QueryEscape(fromImage)
	if tag != "" {
		path += "&tag=" + url.QueryEscape(tag)
	}
	resp, err := dockerRequest(ctx, client, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New(dockerReadError(resp))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func dockerCreateContainer(ctx context.Context, client *http.Client, name string, body any) (*dockerCreateResponse, error) {
	path := "/containers/create?name=" + url.QueryEscape(name)
	resp, err := dockerRequest(ctx, client, http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.New(dockerReadError(resp))
	}
	defer resp.Body.Close()

	var created dockerCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return nil, err
	}
	if created.ID == "" {
		return nil, fmt.Errorf("Docker API 未返回更新器容器 ID")
	}
	return &created, nil
}

func dockerStartContainer(ctx context.Context, client *http.Client, id string) error {
	path := "/containers/" + url.PathEscape(id) + "/start"
	resp, err := dockerRequest(ctx, client, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New(dockerReadError(resp))
	}
	defer resp.Body.Close()
	return nil
}

func splitDockerImage(image string) (string, string) {
	image = strings.TrimSpace(image)
	if image == "" {
		return "", ""
	}
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon > slash {
		return image[:colon], image[colon+1:]
	}
	return image, ""
}

func strconvFormatUnix(t time.Time) string {
	return fmt.Sprintf("%d", t.Unix())
}
