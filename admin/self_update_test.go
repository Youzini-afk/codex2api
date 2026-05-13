package admin

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSplitDockerImage(t *testing.T) {
	tests := []struct {
		image string
		name  string
		tag   string
	}{
		{image: "containrrr/watchtower:latest", name: "containrrr/watchtower", tag: "latest"},
		{image: "ghcr.io/james-6-23/codex2api:latest", name: "ghcr.io/james-6-23/codex2api", tag: "latest"},
		{image: "ghcr.io/james-6-23/codex2api", name: "ghcr.io/james-6-23/codex2api", tag: ""},
		{image: "localhost:5000/codex2api:dev", name: "localhost:5000/codex2api", tag: "dev"},
	}

	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			name, tag := splitDockerImage(tt.image)
			if name != tt.name || tag != tt.tag {
				t.Fatalf("splitDockerImage(%q) = (%q, %q), want (%q, %q)", tt.image, name, tag, tt.name, tt.tag)
			}
		})
	}
}

func TestGetSelfUpdateStatusReportsMissingSocket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CODEX2API_DOCKER_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))

	handler := &Handler{}
	router := gin.New()
	router.GET("/system/update", handler.GetSelfUpdateStatus)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/system/update", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"supported":false`) {
		t.Fatalf("body = %s, want supported=false", body)
	}
	if !strings.Contains(body, "Docker socket") {
		t.Fatalf("body = %s, want missing socket reason", body)
	}
}
