package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/proxy"
)

func TestSettingsCodexForceFastPersistsAndApplies(t *testing.T) {
	previous := proxy.CurrentRuntimeSettings()
	t.Cleanup(func() { proxy.ApplyRuntimeSettings(previous) })
	handler, db, _ := newResponseCacheSettingsAdminHandler(t)

	assertSetting := func(want bool) {
		t.Helper()
		response := invokeResponseCacheSettingsAdmin(t, handler, http.MethodGet, nil)
		var settings map[string]any
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &settings) != nil {
			t.Fatalf("GET settings: %d %s", response.Code, response.Body.String())
		}
		if got := settings["codex_force_fast_enabled"]; got != want {
			t.Fatalf("GET force fast = %v, want %v", got, want)
		}
		persisted, err := db.GetSystemSettings(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if persisted.CodexForceFastEnabled != want || handler.store.CodexForceFastEnabled() != want {
			t.Fatalf("persistent/store force fast mismatch, want %v", want)
		}
		reloaded := auth.NewStore(db, nil, persisted)
		defer reloaded.Stop()
		if reloaded.CodexForceFastEnabled() != want {
			t.Fatalf("force fast lost after store reload, want %v", want)
		}
		if got := proxy.ApplyRuntimeSettingsFromSystem(persisted).CodexForceFastEnabled; got != want {
			t.Fatalf("force fast lost after runtime reload, want %v", want)
		}
	}
	assertSetting(false)
	for _, enabled := range []bool{true, false} {
		response := invokeResponseCacheSettingsAdmin(t, handler, http.MethodPut, map[string]any{"codex_force_fast_enabled": enabled})
		if response.Code != http.StatusOK {
			t.Fatalf("PUT force fast: %d %s", response.Code, response.Body.String())
		}
		if proxy.CurrentRuntimeSettings().CodexForceFastEnabled != enabled {
			t.Fatalf("force fast did not hot apply, want %v", enabled)
		}
		assertSetting(enabled)
		response = invokeResponseCacheSettingsAdmin(t, handler, http.MethodPut, map[string]any{"site_name": "force-fast-test"})
		if response.Code != http.StatusOK {
			t.Fatalf("PUT unrelated setting: %d %s", response.Code, response.Body.String())
		}
		assertSetting(enabled)
	}
}
