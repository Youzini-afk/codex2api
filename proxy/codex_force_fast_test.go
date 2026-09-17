package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/codex2api/auth"
	"github.com/tidwall/gjson"
)

func TestCodexForceFastHTTPAndWebsocket(t *testing.T) {
	previousRuntime := CurrentRuntimeSettings()
	previousResin := resinCfg.Load()
	previousWS := WebsocketExecuteFunc
	t.Cleanup(func() {
		ApplyRuntimeSettings(previousRuntime)
		resinCfg.Store(previousResin)
		WebsocketExecuteFunc = previousWS
	})
	settings := previousRuntime
	settings.CodexForceFastEnabled = true
	settings.CodexFastTierInterceptEnabled = true
	ApplyRuntimeSettings(settings)
	withPayloadRules(t, `{}`)

	bodies := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodies <- readUpstreamRequestBody(r)
		_, _ = w.Write([]byte(`{"id":"resp_test"}`))
	}))
	t.Cleanup(upstream.Close)
	SetResinConfig(&ResinConfig{BaseURL: upstream.URL, PlatformName: "test"})
	WebsocketExecuteFunc = func(_ context.Context, _ *auth.Account, body []byte, _, _, _ string, _ *DeviceProfileConfig, _ http.Header, _ string) (*http.Response, error) {
		bodies <- body
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}

	for _, useWS := range []bool{false, true} {
		transport := "HTTP"
		if useWS {
			transport = "WebSocket"
		}
		for _, tc := range []struct{ name, fields, rules string }{
			{name: "missing"},
			{name: "fast", fields: `,"service_tier":"fast"`},
			{name: "priority", fields: `,"service_tier":"priority"`},
			{name: "default", fields: `,"service_tier":"default"`},
			{name: "flex", fields: `,"service_tier":"flex"`},
			{name: "auto", fields: `,"service_tier":"auto"`},
			{name: "ultrafast", fields: `,"service_tier":"ultrafast"`},
			{name: "camel_case", fields: `,"serviceTier":"default"`},
			{name: "both_fields", fields: `,"service_tier":"fast","serviceTier":"flex"`},
			{name: "payload_override", rules: `{"override":[{"models":["*"],"params":{"service_tier":"default"}}]}`},
			{name: "payload_filter", fields: `,"service_tier":"priority"`, rules: `{"filter":[{"models":["*"],"params":["service_tier"]}]}`},
			{name: "image_tool", fields: `,"tools":[{"type":"image_generation"}]`},
		} {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				rules := tc.rules
				if rules == "" {
					rules = `{}`
				}
				withPayloadRules(t, rules)
				body := []byte(`{"model":"gpt-6","input":"hi"` + tc.fields + `}`)
				resp, err := ExecuteRequest(context.Background(), &auth.Account{DBID: 1, AccessToken: "token"}, body, "", "", "sk-local", nil, http.Header{}, useWS)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				captured := <-bodies
				if got := gjson.GetBytes(captured, "service_tier").String(); got != "priority" || gjson.GetBytes(captured, "serviceTier").Exists() {
					t.Fatalf("upstream tier not forced: %s", captured)
				}
				if got := EffectiveRequestedServiceTier(body, "gpt-6", nil, nil); got != "priority" {
					t.Fatalf("usage requested tier = %q, want priority", got)
				}
			})
		}
	}

	// Turning the switch off restores the existing client/payload behavior.
	settings.CodexForceFastEnabled = false
	ApplyRuntimeSettings(settings)
	for _, raw := range []string{`{"model":"gpt-6"}`, `{"model":"gpt-6","service_tier":"default"}`} {
		if got := string(applyCodexForceFast([]byte(raw))); got != raw {
			t.Fatalf("disabled force changed body: %s", got)
		}
	}
	if got := EffectiveRequestedServiceTier([]byte(`{"service_tier":"default"}`), "gpt-6", nil, nil); got != "default" {
		t.Fatalf("disabled force changed usage tier: %q", got)
	}
}
