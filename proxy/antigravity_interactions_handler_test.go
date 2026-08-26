package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/config"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func TestAntigravityInteractionsHandlerReturnsCanonicalResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv(auth.AntigravityExperimentalInteractionsEnv, "true")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode upstream request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("x-goog-api-key") != "google-api-key" {
			t.Errorf("x-goog-api-key = %q", r.Header.Get("x-goog-api-key"))
		}
		if request["agent"] != antigravityInteractionsAgent {
			t.Errorf("agent = %#v", request["agent"])
		}
		if stream, _ := request["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: interaction.created\n"+
				"data: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"int_stream\",\"object\":\"interaction\",\"model\":\"gemini-3.6-flash-low\",\"status\":\"in_progress\"}}\n\n"+
				"event: step.start\n"+
				"data: {\"event_type\":\"step.start\",\"index\":0,\"step\":{\"type\":\"thought\",\"summary\":[{\"type\":\"text\",\"text\":\"checking\"}]}}\n\n"+
				"event: step.delta\n"+
				"data: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"thought_signature\",\"signature\":\"thought-sig\"}}\n\n"+
				"event: step.stop\n"+
				"data: {\"event_type\":\"step.stop\",\"index\":0}\n\n"+
				"event: step.start\n"+
				"data: {\"event_type\":\"step.start\",\"index\":1,\"step\":{\"type\":\"google_search_call\",\"id\":\"search_1\"}}\n\n"+
				"event: step.stop\n"+
				"data: {\"event_type\":\"step.stop\",\"index\":1}\n\n"+
				"event: step.start\n"+
				"data: {\"event_type\":\"step.start\",\"index\":2,\"step\":{\"type\":\"model_output\"}}\n\n"+
				"event: step.delta\n"+
				"data: {\"event_type\":\"step.delta\",\"index\":2,\"delta\":{\"type\":\"text\",\"text\":\"Hel\"}}\n\n"+
				"event: step.delta\n"+
				"data: {\"event_type\":\"step.delta\",\"index\":2,\"delta\":{\"type\":\"text\",\"text\":\"lo\"}}\n\n"+
				"event: step.stop\n"+
				"data: {\"event_type\":\"step.stop\",\"index\":2}\n\n"+
				"event: step.start\n"+
				"data: {\"event_type\":\"step.start\",\"index\":3,\"step\":{\"type\":\"function_call\",\"id\":\"call_stream\",\"name\":\"lookup\",\"arguments\":{}}}\n\n"+
				"event: step.delta\n"+
				"data: {\"event_type\":\"step.delta\",\"index\":3,\"delta\":{\"type\":\"arguments_delta\",\"arguments\":\"{\\\"city\\\":\\\"Paris\\\"}\"}}\n\n"+
				"event: step.stop\n"+
				"data: {\"event_type\":\"step.stop\",\"index\":3}\n\n"+
				"event: interaction.completed\n"+
				"data: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"int_stream\",\"status\":\"requires_action\",\"usage\":{\"total_input_tokens\":5,\"total_output_tokens\":7,\"total_thought_tokens\":3,\"total_cached_tokens\":2,\"total_tokens\":15}}}\n\n"+
				"event: done\n"+
				"data: [DONE]\n\n")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"int_json","object":"interaction","model":"gemini-3.6-flash-low","status":"completed",
			"steps":[
				{"type":"thought","signature":"thought-sig","summary":[{"type":"text","text":"checking"}]},
				{"type":"google_search_call","id":"search_1","arguments":{"query":"hello"}},
				{"type":"google_search_result","call_id":"search_1","result":{}},
				{"type":"model_output","content":[{"type":"text","text":"Hello"}]},
				{"type":"function_call","id":"call_json","name":"lookup","arguments":{"city":"Paris"}}
			],
			"usage":{"total_input_tokens":5,"total_output_tokens":7,"total_thought_tokens":3,"total_cached_tokens":2,"total_tokens":15}
		}`)
	}))
	defer upstream.Close()

	previousEndpoint := antigravityInteractionsEndpoint
	antigravityInteractionsEndpoint = upstream.URL + "/v1beta/interactions"
	t.Cleanup(func() { antigravityInteractionsEndpoint = previousEndpoint })

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, MaxRetries: 0})
	store.AddAccount(&auth.Account{
		DBID: 901, UpstreamType: auth.UpstreamAntigravity, APIKey: "google-api-key",
		Models: []string{"gemini-3.6-flash-low"}, HealthTier: auth.HealthTierHealthy, Status: auth.StatusReady,
	})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	router := gin.New()
	handler.RegisterRoutes(router)

	t.Run("json text thought function and usage", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gemini-3.6-flash-low","input":"hello","stream":false}`))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
		}
		body := recorder.Body.Bytes()
		if gjson.GetBytes(body, "object").String() != "response" || gjson.GetBytes(body, "status").String() != "completed" {
			t.Fatalf("non-canonical response: %s", body)
		}
		if gjson.GetBytes(body, "output_text").String() != "Hello" {
			t.Fatalf("output_text missing: %s", body)
		}
		createdAt := gjson.GetBytes(body, "created_at").Int()
		if createdAt <= 0 || createdAt > time.Now().Unix() {
			t.Fatalf("created_at = %d, want current Unix seconds", createdAt)
		}
		if gjson.GetBytes(body, "usage.input_tokens").Int() != 5 || gjson.GetBytes(body, "usage.output_tokens").Int() != 10 || gjson.GetBytes(body, "usage.output_tokens_details.reasoning_tokens").Int() != 3 || gjson.GetBytes(body, "usage.input_tokens_details.cached_tokens").Int() != 2 || gjson.GetBytes(body, "usage.total_tokens").Int() != 15 {
			t.Fatalf("usage projection is wrong: %s", body)
		}
		output := gjson.GetBytes(body, "output").Array()
		if len(output) != 3 || output[0].Get("type").String() != "reasoning" || output[1].Get("type").String() != "message" || output[2].Get("type").String() != "function_call" {
			t.Fatalf("portable steps were not preserved in order: %s", body)
		}
		if output[0].Get("summary.0.text").String() != "checking" || output[0].Get("encrypted_content").String() != "thought-sig" {
			t.Fatalf("thought projection is incomplete: %s", output[0].Raw)
		}
		if output[2].Get("call_id").String() != "call_json" || output[2].Get("name").String() != "lookup" || output[2].Get("arguments").String() != `{"city":"Paris"}` {
			t.Fatalf("function projection is incomplete: %s", output[2].Raw)
		}
		if bytes.Contains(body, []byte("google_search")) || bytes.Contains(body, []byte(`"object":"interaction"`)) || bytes.Contains(body, []byte(`"steps"`)) {
			t.Fatalf("raw Interactions envelope leaked downstream: %s", body)
		}
	})

	t.Run("sse deltas terminal and usage", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gemini-3.6-flash-low","input":"hello","stream":true}`))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
		}
		var eventTypes []string
		var text strings.Builder
		var terminal gjson.Result
		if err := ReadSSEStream(bytes.NewReader(recorder.Body.Bytes()), func(data []byte) bool {
			event := gjson.ParseBytes(data)
			eventType := event.Get("type").String()
			eventTypes = append(eventTypes, eventType)
			if eventType == "response.output_text.delta" {
				text.WriteString(event.Get("delta").String())
			}
			if eventType == "response.completed" {
				terminal = event
			}
			return true
		}); err != nil {
			t.Fatal(err)
		}
		if text.String() != "Hello" {
			t.Fatalf("streamed text = %q; body=%s", text.String(), recorder.Body.String())
		}
		for _, required := range []string{"response.reasoning_summary_text.delta", "response.output_text.delta", "response.function_call_arguments.delta", "response.completed"} {
			found := false
			for _, eventType := range eventTypes {
				if eventType == required {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("missing %s; events=%v body=%s", required, eventTypes, recorder.Body.String())
			}
		}
		if !terminal.Exists() || terminal.Get("response.status").String() != "completed" || terminal.Get("response.usage.input_tokens").Int() != 5 || terminal.Get("response.usage.output_tokens").Int() != 10 || terminal.Get("response.usage.total_tokens").Int() != 15 {
			t.Fatalf("terminal/usage projection is wrong: %s", terminal.Raw)
		}
		if terminal.Get("response.output.2.type").String() != "function_call" || terminal.Get("response.output.2.call_id").String() != "call_stream" {
			t.Fatalf("stream function call missing from terminal output: %s", terminal.Raw)
		}
		if strings.Contains(recorder.Body.String(), "upstream_stream_break") || strings.Contains(recorder.Body.String(), `"event_type"`) || strings.Contains(recorder.Body.String(), `"object":"interaction"`) {
			t.Fatalf("raw/false-failure Interactions data leaked downstream: %s", recorder.Body.String())
		}
	})
}
