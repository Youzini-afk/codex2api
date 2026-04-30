package admin

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/codex2api/security"
	"github.com/gin-gonic/gin"
)

const (
	adminSessionCookieName      = "codex2api_admin_session"
	adminSessionTTL             = 24 * time.Hour
	adminSessionCleanupInterval = 10 * time.Minute
)

type adminSessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time
}

func newAdminSessionStore() *adminSessionStore {
	store := &adminSessionStore{
		sessions: make(map[string]time.Time),
	}
	go store.cleanupLoop()
	return store
}

func (s *adminSessionStore) Create() (string, time.Time, error) {
	token, err := adminRandomHex(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().Add(adminSessionTTL)

	s.mu.Lock()
	s.sessions[token] = expiresAt
	s.mu.Unlock()

	return token, expiresAt, nil
}

func (s *adminSessionStore) Validate(token string) (time.Time, bool) {
	if s == nil || strings.TrimSpace(token) == "" {
		return time.Time{}, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	expiresAt, ok := s.sessions[token]
	if !ok {
		return time.Time{}, false
	}
	if time.Now().After(expiresAt) {
		delete(s.sessions, token)
		return time.Time{}, false
	}
	return expiresAt, true
}

func (s *adminSessionStore) Delete(token string) {
	if s == nil || strings.TrimSpace(token) == "" {
		return
	}
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

func (s *adminSessionStore) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.sessions = make(map[string]time.Time)
	s.mu.Unlock()
}

func (s *adminSessionStore) cleanupLoop() {
	ticker := time.NewTicker(adminSessionCleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		s.mu.Lock()
		for token, expiresAt := range s.sessions {
			if now.After(expiresAt) {
				delete(s.sessions, token)
			}
		}
		s.mu.Unlock()
	}
}

func adminRandomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

type adminSessionStatusResponse struct {
	AuthRequired    bool   `json:"auth_required"`
	Authenticated   bool   `json:"authenticated"`
	AdminAuthSource string `json:"admin_auth_source"`
	AuthMethod      string `json:"auth_method"`
	ExpiresAt       string `json:"expires_at,omitempty"`
}

func (h *Handler) authorizeAdminRequest(c *gin.Context) (bool, string, string) {
	adminSecret, source := h.resolveAdminSecret(c.Request.Context())
	if adminSecret == "" {
		return false, source, ""
	}

	if expiresAt, ok := h.validateAdminSession(c.Request); ok {
		c.Set("admin_session_expires_at", expiresAt.Format(time.RFC3339))
		return true, source, "session"
	}

	adminKey := c.GetHeader("X-Admin-Key")
	if adminKey == "" {
		authHeader := c.GetHeader("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			adminKey = strings.TrimPrefix(authHeader, "Bearer ")
		}
	}
	adminKey = security.SanitizeInput(adminKey)
	if security.SecureCompare(adminKey, adminSecret) {
		return true, source, "header"
	}

	return false, source, ""
}

func (h *Handler) validateAdminSession(r *http.Request) (time.Time, bool) {
	if h == nil || h.sessionStore == nil || r == nil {
		return time.Time{}, false
	}
	cookie, err := r.Cookie(adminSessionCookieName)
	if err != nil || cookie == nil {
		return time.Time{}, false
	}
	return h.sessionStore.Validate(security.SanitizeInput(cookie.Value))
}

func (h *Handler) GetAdminSessionStatus(c *gin.Context) {
	adminSecret, source := h.resolveAdminSecret(c.Request.Context())
	if adminSecret == "" {
		c.JSON(http.StatusOK, adminSessionStatusResponse{
			AuthRequired:    false,
			Authenticated:   true,
			AdminAuthSource: "disabled",
			AuthMethod:      "disabled",
		})
		return
	}

	if expiresAt, ok := h.validateAdminSession(c.Request); ok {
		c.JSON(http.StatusOK, adminSessionStatusResponse{
			AuthRequired:    true,
			Authenticated:   true,
			AdminAuthSource: source,
			AuthMethod:      "session",
			ExpiresAt:       expiresAt.Format(time.RFC3339),
		})
		return
	}

	adminKey := c.GetHeader("X-Admin-Key")
	if adminKey == "" {
		authHeader := c.GetHeader("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			adminKey = strings.TrimPrefix(authHeader, "Bearer ")
		}
	}
	adminKey = security.SanitizeInput(adminKey)
	if security.SecureCompare(adminKey, adminSecret) {
		c.JSON(http.StatusOK, adminSessionStatusResponse{
			AuthRequired:    true,
			Authenticated:   true,
			AdminAuthSource: source,
			AuthMethod:      "header",
		})
		return
	}

	c.JSON(http.StatusOK, adminSessionStatusResponse{
		AuthRequired:    true,
		Authenticated:   false,
		AdminAuthSource: source,
		AuthMethod:      "none",
	})
}

func (h *Handler) LoginAdminSession(c *gin.Context) {
	var req struct {
		Secret string `json:"secret"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "请求格式错误")
		return
	}

	adminSecret, source := h.resolveAdminSecret(c.Request.Context())
	if adminSecret == "" {
		c.JSON(http.StatusOK, adminSessionStatusResponse{
			AuthRequired:    false,
			Authenticated:   true,
			AdminAuthSource: "disabled",
			AuthMethod:      "disabled",
		})
		return
	}

	secret := security.SanitizeInput(strings.TrimSpace(req.Secret))
	if !security.SecureCompare(secret, adminSecret) {
		security.SecurityAuditLog("ADMIN_LOGIN_FAILED", fmt.Sprintf("ip=%s source=%s", c.ClientIP(), source))
		writeError(c, http.StatusUnauthorized, "管理密钥错误")
		return
	}
	if h.sessionStore == nil {
		h.sessionStore = newAdminSessionStore()
	}

	token, expiresAt, err := h.sessionStore.Create()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "创建登录会话失败")
		return
	}

	setAdminSessionCookie(c, token, expiresAt)
	security.SecurityAuditLog("ADMIN_LOGIN_SUCCESS", fmt.Sprintf("ip=%s source=%s", c.ClientIP(), source))
	c.JSON(http.StatusOK, adminSessionStatusResponse{
		AuthRequired:    true,
		Authenticated:   true,
		AdminAuthSource: source,
		AuthMethod:      "session",
		ExpiresAt:       expiresAt.Format(time.RFC3339),
	})
}

func (h *Handler) LogoutAdminSession(c *gin.Context) {
	if h.sessionStore != nil {
		if cookie, err := c.Request.Cookie(adminSessionCookieName); err == nil && cookie != nil {
			h.sessionStore.Delete(security.SanitizeInput(cookie.Value))
		}
	}
	clearAdminSessionCookie(c)
	c.JSON(http.StatusOK, gin.H{"message": "已退出登录"})
}

func setAdminSessionCookie(c *gin.Context, token string, expiresAt time.Time) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(c.Request),
		SameSite: http.SameSiteStrictMode,
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
	})
}

func clearAdminSessionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(c.Request),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

func isSecureRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}
