package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestLoginAdminSessionSetsCookieAndAllowsProtectedAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := &Handler{
		adminSecretEnv: "super-secret",
		sessionStore:   newAdminSessionStore(),
	}
	router := newAdminAuthTestRouter(handler)

	loginBody := bytes.NewBufferString(`{"secret":"super-secret"}`)
	loginReq := httptest.NewRequest(http.MethodPost, "/api/admin/auth/login", loginBody)
	loginReq.Header.Set("Content-Type", "application/json")
	loginResp := httptest.NewRecorder()
	router.ServeHTTP(loginResp, loginReq)

	if loginResp.Code != http.StatusOK {
		t.Fatalf("login status = %d, want %d", loginResp.Code, http.StatusOK)
	}
	cookies := loginResp.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected login to set session cookie")
	}

	protectedReq := httptest.NewRequest(http.MethodGet, "/api/admin/protected", nil)
	protectedReq.AddCookie(cookies[0])
	protectedResp := httptest.NewRecorder()
	router.ServeHTTP(protectedResp, protectedReq)

	if protectedResp.Code != http.StatusOK {
		t.Fatalf("protected status = %d, want %d", protectedResp.Code, http.StatusOK)
	}
}

func TestLoginAdminSessionRejectsInvalidSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := &Handler{
		adminSecretEnv: "super-secret",
		sessionStore:   newAdminSessionStore(),
	}
	router := newAdminAuthTestRouter(handler)

	loginBody := bytes.NewBufferString(`{"secret":"wrong-secret"}`)
	loginReq := httptest.NewRequest(http.MethodPost, "/api/admin/auth/login", loginBody)
	loginReq.Header.Set("Content-Type", "application/json")
	loginResp := httptest.NewRecorder()
	router.ServeHTTP(loginResp, loginReq)

	if loginResp.Code != http.StatusUnauthorized {
		t.Fatalf("login status = %d, want %d", loginResp.Code, http.StatusUnauthorized)
	}
	assertErrorMessage(t, loginResp, "管理密钥错误")
}

func TestGetAdminSessionStatusDisabledWithoutSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := &Handler{
		db:           newTestAdminDB(t),
		sessionStore: newAdminSessionStore(),
	}
	router := newAdminAuthTestRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/auth/status", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}

	var payload adminSessionStatusResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.AuthRequired {
		t.Fatal("expected auth to be disabled")
	}
	if !payload.Authenticated {
		t.Fatal("expected disabled auth to report authenticated")
	}
}

func newAdminAuthTestRouter(handler *Handler) *gin.Engine {
	router := gin.New()
	authAPI := router.Group("/api/admin/auth")
	authAPI.GET("/status", handler.GetAdminSessionStatus)
	authAPI.POST("/login", handler.LoginAdminSession)
	authAPI.POST("/logout", handler.LogoutAdminSession)

	protected := router.Group("/api/admin")
	protected.Use(handler.adminAuthMiddleware())
	protected.GET("/protected", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	})

	return router
}
