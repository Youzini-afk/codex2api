package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

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

func TestAdminSessionsRejectDatabaseSecretRotationAcrossInstances(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newTestAdminDB(t)
	settings := defaultBootstrapSettings()
	settings.AdminSecret = "database-secret-v1"
	if err := db.UpdateSystemSettings(context.Background(), settings); err != nil {
		t.Fatalf("seed admin secret: %v", err)
	}

	handlerA := &Handler{db: db, sessionStore: newAdminSessionStore()}
	handlerB := &Handler{db: db, sessionStore: newAdminSessionStore()}
	routerA := newAdminAuthTestRouter(handlerA)
	routerB := newAdminAuthTestRouter(handlerB)
	cookieA := loginAdminSessionForTest(t, routerA, settings.AdminSecret)
	cookieB := loginAdminSessionForTest(t, routerB, settings.AdminSecret)

	assertAdminSessionAccessForTest(t, routerA, cookieA, http.StatusOK)
	assertAdminSessionAccessForTest(t, routerB, cookieB, http.StatusOK)

	// Rewriting the same effective secret must not invalidate either instance.
	if err := db.UpdateSystemSettings(context.Background(), settings); err != nil {
		t.Fatalf("rewrite unchanged admin secret: %v", err)
	}
	assertAdminSessionAccessForTest(t, routerA, cookieA, http.StatusOK)
	assertAdminSessionAccessForTest(t, routerB, cookieB, http.StatusOK)

	settings.AdminSecret = "database-secret-v2"
	if err := db.UpdateSystemSettings(context.Background(), settings); err != nil {
		t.Fatalf("rotate admin secret: %v", err)
	}

	assertAdminSessionAccessForTest(t, routerA, cookieA, http.StatusUnauthorized)
	assertAdminSessionAccessForTest(t, routerB, cookieB, http.StatusUnauthorized)
	assertAdminHeaderAccessForTest(t, routerA, "database-secret-v1", http.StatusUnauthorized)
	assertAdminHeaderAccessForTest(t, routerA, "database-secret-v2", http.StatusOK)
}

func TestAdminSessionUsesEffectiveEnvironmentSecretFingerprint(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newTestAdminDB(t)
	settings := defaultBootstrapSettings()
	settings.AdminSecret = "database-secret-v1"
	if err := db.UpdateSystemSettings(context.Background(), settings); err != nil {
		t.Fatalf("seed admin secret: %v", err)
	}

	store := newAdminSessionStore()
	handler := &Handler{
		db:             db,
		adminSecretEnv: "environment-secret-v1",
		sessionStore:   store,
	}
	router := newAdminAuthTestRouter(handler)
	cookie := loginAdminSessionForTest(t, router, handler.adminSecretEnv)

	settings.AdminSecret = "database-secret-v2"
	if err := db.UpdateSystemSettings(context.Background(), settings); err != nil {
		t.Fatalf("rotate shadowed database secret: %v", err)
	}
	assertAdminSessionAccessForTest(t, router, cookie, http.StatusOK)

	// Sharing the store injects a changed effective environment secret without
	// relying on a process restart, which would discard the local store anyway.
	changedEnvHandler := &Handler{
		db:             db,
		adminSecretEnv: "environment-secret-v2",
		sessionStore:   store,
	}
	assertAdminSessionAccessForTest(t, newAdminAuthTestRouter(changedEnvHandler), cookie, http.StatusUnauthorized)
}

func TestAdminSessionStoreDoesNotRetainPlaintextSecret(t *testing.T) {
	store := newAdminSessionStore()
	secret := "plaintext-admin-secret"
	token, _, err := store.Create(secret)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	store.mu.Lock()
	session := store.sessions[token]
	store.mu.Unlock()
	if bytes.Contains([]byte(token), []byte(secret)) {
		t.Fatal("session token contains plaintext admin secret")
	}
	if bytes.Contains(session.secretFingerprint[:], []byte(secret)) {
		t.Fatal("session record contains plaintext admin secret")
	}
	if _, ok := store.Validate(token, secret); !ok {
		t.Fatal("session should validate with the issuing secret")
	}
	if _, ok := store.Validate(token, "rotated-admin-secret"); ok {
		t.Fatal("session should not validate with a different secret")
	}
}

func TestAdminSessionStoreRejectsDeletedAndExpiredSessions(t *testing.T) {
	store := newAdminSessionStore()
	const secret = "admin-secret"

	deletedToken, _, err := store.Create(secret)
	if err != nil {
		t.Fatalf("create session for delete: %v", err)
	}
	store.Delete(deletedToken)
	if _, ok := store.Validate(deletedToken, secret); ok {
		t.Fatal("deleted session should not validate")
	}

	expiredToken, _, err := store.Create(secret)
	if err != nil {
		t.Fatalf("create session for expiry: %v", err)
	}
	store.mu.Lock()
	expired := store.sessions[expiredToken]
	expired.expiresAt = time.Now().Add(-time.Second)
	store.sessions[expiredToken] = expired
	store.mu.Unlock()
	if _, ok := store.Validate(expiredToken, secret); ok {
		t.Fatal("expired session should not validate")
	}
}

func TestAdminSessionStoreConcurrentCreateAndValidate(t *testing.T) {
	store := newAdminSessionStore()
	const workers = 32

	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			secret := fmt.Sprintf("admin-secret-%d", worker)
			token, _, err := store.Create(secret)
			if err != nil {
				errs <- fmt.Errorf("worker %d create: %w", worker, err)
				return
			}
			if _, ok := store.Validate(token, secret); !ok {
				errs <- fmt.Errorf("worker %d same-secret validation failed", worker)
				return
			}
			if _, ok := store.Validate(token, secret+"-rotated"); ok {
				errs <- fmt.Errorf("worker %d rotated-secret validation succeeded", worker)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestGetAdminSessionStatusRequiresBootstrapWithoutSecret(t *testing.T) {
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
	if !payload.AuthRequired {
		t.Fatal("expected admin auth to remain required before bootstrap")
	}
	if payload.Authenticated {
		t.Fatal("expected unauthenticated status before bootstrap")
	}
	if payload.AuthMethod != "none" {
		t.Fatalf("auth method = %q, want none", payload.AuthMethod)
	}
}

func TestLoginAdminSessionRequiresBootstrapWithoutSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := &Handler{
		db:           newTestAdminDB(t),
		sessionStore: newAdminSessionStore(),
	}
	router := newAdminAuthTestRouter(handler)

	loginReq := httptest.NewRequest(http.MethodPost, "/api/admin/auth/login", bytes.NewBufferString(`{"secret":"anything"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginResp := httptest.NewRecorder()
	router.ServeHTTP(loginResp, loginReq)

	if loginResp.Code != http.StatusServiceUnavailable {
		t.Fatalf("login status = %d, want %d", loginResp.Code, http.StatusServiceUnavailable)
	}
	if !bytes.Contains(loginResp.Body.Bytes(), []byte(`"code":"bootstrap_required"`)) {
		t.Fatalf("login response = %s, want bootstrap_required", loginResp.Body.String())
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

func loginAdminSessionForTest(t *testing.T, router http.Handler, secret string) *http.Cookie {
	t.Helper()

	loginBody, err := json.Marshal(map[string]string{"secret": secret})
	if err != nil {
		t.Fatalf("marshal login body: %v", err)
	}
	loginReq := httptest.NewRequest(http.MethodPost, "/api/admin/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginResp := httptest.NewRecorder()
	router.ServeHTTP(loginResp, loginReq)
	if loginResp.Code != http.StatusOK {
		t.Fatalf("login status = %d, want %d: %s", loginResp.Code, http.StatusOK, loginResp.Body.String())
	}
	for _, cookie := range loginResp.Result().Cookies() {
		if cookie.Name == adminSessionCookieName {
			return cookie
		}
	}
	t.Fatal("login did not set admin session cookie")
	return nil
}

func assertAdminSessionAccessForTest(t *testing.T, router http.Handler, cookie *http.Cookie, wantStatus int) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/protected", nil)
	req.AddCookie(cookie)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != wantStatus {
		t.Fatalf("protected status = %d, want %d: %s", resp.Code, wantStatus, resp.Body.String())
	}
}

func assertAdminHeaderAccessForTest(t *testing.T, router http.Handler, secret string, wantStatus int) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/protected", nil)
	req.Header.Set("X-Admin-Key", secret)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != wantStatus {
		t.Fatalf("protected header status = %d, want %d: %s", resp.Code, wantStatus, resp.Body.String())
	}
}
