package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

func TestParseImportJSONTokensSupportsFlatObjectWithBOM(t *testing.T) {
	data := append([]byte{0xef, 0xbb, 0xbf}, []byte(`{"refresh_token":"rt-flat","email":"flat@example.com"}`)...)

	tokens, err := parseImportJSONTokens(data)
	if err != nil {
		t.Fatalf("parseImportJSONTokens returned error: %v", err)
	}

	if len(tokens) != 1 {
		t.Fatalf("tokens len = %d, want 1", len(tokens))
	}
	if tokens[0].refreshToken != "rt-flat" {
		t.Fatalf("refreshToken = %q, want %q", tokens[0].refreshToken, "rt-flat")
	}
	if tokens[0].name != "flat@example.com" {
		t.Fatalf("name = %q, want %q", tokens[0].name, "flat@example.com")
	}
	if tokens[0].accessToken != "" {
		t.Fatalf("accessToken = %q, want empty", tokens[0].accessToken)
	}
}

func TestParseImportJSONTokensSupportsFlatArray(t *testing.T) {
	data := []byte(`[
		{"refresh_token":"rt-1","email":"one@example.com"},
		{"access_token":"at-2","email":"two@example.com"},
		{"refresh_token":"","access_token":"","email":"ignored@example.com"}
	]`)

	tokens, err := parseImportJSONTokens(data)
	if err != nil {
		t.Fatalf("parseImportJSONTokens returned error: %v", err)
	}

	if len(tokens) != 2 {
		t.Fatalf("tokens len = %d, want 2", len(tokens))
	}
	if tokens[0].refreshToken != "rt-1" || tokens[0].name != "one@example.com" {
		t.Fatalf("first token = %+v, want rt-1 / one@example.com", tokens[0])
	}
	if tokens[1].accessToken != "at-2" || tokens[1].name != "two@example.com" {
		t.Fatalf("second token = %+v, want at-2 / two@example.com", tokens[1])
	}
}

func TestParseImportJSONTokensSupportsSub2API(t *testing.T) {
	data := []byte(`{
		"exported_at": "2026-04-03T14:49:53Z",
		"proxies": [
			{"proxy_key":"http|10.0.1.4|80|user|pass","name":"ignored proxy"}
		],
		"accounts": [
			{
				"name": "Primary Account",
				"proxy_key": "http|10.0.1.4|80|user|pass",
				"credentials": {
					"refresh_token": "rt-primary",
					"access_token": "at-primary",
					"email": "primary@example.com"
				},
				"extra": {"ignored": true}
			},
			{
				"credentials": {
					"access_token": "at-email-fallback",
					"email": "fallback@example.com"
				}
			},
			{
				"credentials": {
					"access_token": "at-default-name"
				}
			},
			{
				"name": "Ignored Account",
				"credentials": {}
			}
		]
	}`)

	tokens, err := parseImportJSONTokens(data)
	if err != nil {
		t.Fatalf("parseImportJSONTokens returned error: %v", err)
	}

	if len(tokens) != 3 {
		t.Fatalf("tokens len = %d, want 3", len(tokens))
	}

	if tokens[0].refreshToken != "rt-primary" {
		t.Fatalf("first refreshToken = %q, want %q", tokens[0].refreshToken, "rt-primary")
	}
	if tokens[0].accessToken != "at-primary" {
		t.Fatalf("first accessToken = %q, want %q", tokens[0].accessToken, "at-primary")
	}
	if tokens[0].name != "Primary Account" {
		t.Fatalf("first name = %q, want %q", tokens[0].name, "Primary Account")
	}

	if tokens[1].accessToken != "at-email-fallback" || tokens[1].name != "fallback@example.com" {
		t.Fatalf("second token = %+v, want access token with email fallback", tokens[1])
	}

	if tokens[2].accessToken != "at-default-name" || tokens[2].name != "" {
		t.Fatalf("third token = %+v, want access token with empty name for default naming", tokens[2])
	}
}

func TestParseImportJSONTokensSupportsSub2APISharedAccountDifferentUsers(t *testing.T) {
	data := []byte(`{
		"exported_at": "2026-04-03T14:49:53Z",
		"accounts": [
			{
				"name": "Team User One",
				"credentials": {
					"chatgpt_account_id": "team-workspace",
					"chatgpt_user_id": "user-one",
					"email": "one@example.com",
					"access_token": "at-one"
				}
			},
			{
				"name": "Team User Two",
				"credentials": {
					"chatgpt_account_id": "team-workspace",
					"user_id": "user-two",
					"email": "two@example.com",
					"access_token": "at-two"
				}
			}
		]
	}`)

	tokens, err := parseImportJSONTokens(data)
	if err != nil {
		t.Fatalf("parseImportJSONTokens returned error: %v", err)
	}

	if len(tokens) != 2 {
		t.Fatalf("tokens len = %d, want 2", len(tokens))
	}
	if tokens[0].chatgptAccountID != "team-workspace" || tokens[1].chatgptAccountID != "team-workspace" {
		t.Fatalf("chatgptAccountID = %q/%q, want shared workspace", tokens[0].chatgptAccountID, tokens[1].chatgptAccountID)
	}
	if tokens[0].userID != "user-one" || tokens[1].userID != "user-two" {
		t.Fatalf("userID = %q/%q, want distinct imported user ids", tokens[0].userID, tokens[1].userID)
	}
	if tokens[0].accessToken != "at-one" || tokens[1].accessToken != "at-two" {
		t.Fatalf("accessToken = %q/%q, want distinct ATs", tokens[0].accessToken, tokens[1].accessToken)
	}
}

func TestParseImportJSONTokensSupportsSub2APINumericExpiresAt(t *testing.T) {
	data := []byte(`{
		"accounts": [
			{
				"name": "Numeric Expiry",
				"credentials": {
					"refresh_token": "rt-numeric",
					"access_token": "at-numeric",
					"expires_at": 1779071020
				}
			}
		]
	}`)

	tokens, err := parseImportJSONTokens(data)
	if err != nil {
		t.Fatalf("parseImportJSONTokens returned error: %v", err)
	}

	if len(tokens) != 1 {
		t.Fatalf("tokens len = %d, want 1", len(tokens))
	}
	if tokens[0].expiresAt != "1779071020" {
		t.Fatalf("expiresAt = %q, want numeric value preserved", tokens[0].expiresAt)
	}
}

func TestFetchSub2APISummariesDefaultsMissingDataPlatformToOpenAI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/admin/accounts":
			_ = json.NewEncoder(w).Encode(sub2apiEnvelope{
				Data: json.RawMessage(`{
					"items": [{"name":"Team User","platform":" OpenAI ","status":"active"}],
					"total": 1,
					"page": 1,
					"page_size": 200
				}`),
			})
		case "/api/v1/admin/accounts/data":
			_ = json.NewEncoder(w).Encode(sub2apiEnvelope{
				Data: json.RawMessage(`{
					"accounts": [{
						"name":"Team User",
						"credentials": {
							"chatgpt_account_id":"team-workspace",
							"chatgpt_user_id":"user-one",
							"email":"one@example.com",
							"access_token":"at-one"
						}
					}]
				}`),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	summaries, err := fetchSub2APISummaries(context.Background(), server.URL, "test-key")
	if err != nil {
		t.Fatalf("fetchSub2APISummaries returned error: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("summaries len = %d, want 1", len(summaries))
	}
	if summaries[0].Platform != "openai" || summaries[0].Status != "active" {
		t.Fatalf("summary platform/status = %q/%q, want openai/active", summaries[0].Platform, summaries[0].Status)
	}
	if summaries[0].ChatGPTAccountID != "team-workspace" || summaries[0].Email != "one@example.com" {
		t.Fatalf("summary identity = %+v, want workspace/email preserved", summaries[0])
	}

	tok, ok := sub2apiAccountToImportToken(summaries[0])
	if !ok {
		t.Fatal("sub2apiAccountToImportToken returned !ok")
	}
	if tok.userID != "user-one" || tok.accessToken != "at-one" {
		t.Fatalf("token = %+v, want user id and access token preserved", tok)
	}
}

func TestParseCredentialExpiresAtSupportsUnixSeconds(t *testing.T) {
	got := parseCredentialExpiresAt("1779071020").UTC()
	want := time.Unix(1779071020, 0).UTC()
	if !got.Equal(want) {
		t.Fatalf("parseCredentialExpiresAt = %s, want %s", got, want)
	}
}

func TestParseImportJSONTokensPreservesCPAFields(t *testing.T) {
	data := []byte(`{
		"type": "codex",
		"email": "cpa@example.com",
		"plan_type": "free",
		"codex_7d_used_percent": 3,
		"codex_7d_reset_at": "2026-05-15T20:33:11+08:00",
		"codex_5h_used_percent": 0,
		"codex_5h_reset_at": "2026-05-11T11:39:07+08:00",
		"codex_usage_updated_at": "2026-05-11T11:39:07+08:00",
		"expired": "2026-04-25T12:00:00Z",
		"id_token": "id-cpa",
		"account_id": "acc-cpa",
		"access_token": "at-cpa",
		"refresh_token": "rt-cpa"
	}`)

	tokens, err := parseImportJSONTokens(data)
	if err != nil {
		t.Fatalf("parseImportJSONTokens returned error: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("tokens len = %d, want 1", len(tokens))
	}

	token := tokens[0]
	if token.refreshToken != "rt-cpa" || token.accessToken != "at-cpa" {
		t.Fatalf("token = %+v, want RT and AT preserved", token)
	}
	if token.email != "cpa@example.com" || token.name != "cpa@example.com" {
		t.Fatalf("identity = name:%q email:%q, want cpa@example.com", token.name, token.email)
	}
	if token.planType != "free" {
		t.Fatalf("planType = %q, want free", token.planType)
	}
	if token.codex7DUsedPercent != "3" || token.codex7DResetAt != "2026-05-15T20:33:11+08:00" {
		t.Fatalf("7d usage = %q/%q, want 3/reset", token.codex7DUsedPercent, token.codex7DResetAt)
	}
	if token.codex5HUsedPercent != "0" || token.codex5HResetAt != "2026-05-11T11:39:07+08:00" {
		t.Fatalf("5h usage = %q/%q, want 0/reset", token.codex5HUsedPercent, token.codex5HResetAt)
	}
	if token.codexUsageUpdatedAt != "2026-05-11T11:39:07+08:00" {
		t.Fatalf("usageUpdatedAt = %q, want timestamp", token.codexUsageUpdatedAt)
	}
	if token.idToken != "id-cpa" || token.accountID != "acc-cpa" || token.expiresAt != "2026-04-25T12:00:00Z" {
		t.Fatalf("metadata = %+v, want CPA token metadata preserved", token)
	}
}

func TestAccountFromCredentialSeedRestoresUsageSnapshots(t *testing.T) {
	account := accountFromCredentialSeed(42, "", tokenCredentialSeed{
		planType:            "free",
		codex7DUsedPercent:  "3",
		codex7DResetAt:      "2026-05-15T20:33:11+08:00",
		codex5HUsedPercent:  "0",
		codex5HResetAt:      "2026-05-11T11:39:07+08:00",
		codexUsageUpdatedAt: "2026-05-11T11:39:07+08:00",
	})

	if got := account.GetPlanType(); got != "free" {
		t.Fatalf("PlanType = %q, want free", got)
	}
	pct7d, ok := account.GetUsagePercent7d()
	if !ok || pct7d != 3 {
		t.Fatalf("7d usage = %v/%t, want 3/true", pct7d, ok)
	}
	if account.GetReset7dAt().IsZero() {
		t.Fatal("Reset7dAt is zero")
	}
	pct5h, ok := account.GetUsagePercent5h()
	if !ok || pct5h != 0 {
		t.Fatalf("5h usage = %v/%t, want 0/true", pct5h, ok)
	}
	if account.GetReset5hAt().IsZero() {
		t.Fatal("Reset5hAt is zero")
	}
}

func TestParseImportJSONTokensReturnsNoTokensForValidUnsupportedJSON(t *testing.T) {
	data := []byte(`{"accounts":[{"credentials":{}}],"proxies":[{"proxy_key":"ignored"}]}`)

	tokens, err := parseImportJSONTokens(data)
	if err != nil {
		t.Fatalf("parseImportJSONTokens returned error: %v", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("tokens len = %d, want 0", len(tokens))
	}
}

func TestParseImportJSONTokensRejectsInvalidJSON(t *testing.T) {
	if _, err := parseImportJSONTokens([]byte(`{"accounts":[}`)); err == nil {
		t.Fatal("expected invalid JSON error, got nil")
	}
}

func TestImportTokensFromTextFilesReadsAllUploadedFiles(t *testing.T) {
	files := []uploadedImportFile{
		{name: "one.txt", data: append([]byte{0xef, 0xbb, 0xbf}, []byte("rt-1\nrt-shared\n")...)},
		{name: "two.txt", data: []byte("rt-2\nrt-shared\n")},
	}

	tokens := importTokensFromTextFiles(files, func(token string) importToken {
		return importToken{refreshToken: token}
	})

	if len(tokens) != 3 {
		t.Fatalf("tokens len = %d, want 3", len(tokens))
	}
	for i, want := range []string{"rt-1", "rt-shared", "rt-2"} {
		if tokens[i].refreshToken != want {
			t.Fatalf("tokens[%d] = %q, want %q", i, tokens[i].refreshToken, want)
		}
	}
}

func TestReadUploadedImportFilesReadsRepeatedFileFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	req := newMultipartRequest(t, map[string]string{
		"one.txt": "rt-1",
		"two.txt": "rt-2",
	})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = req

	files, err := readUploadedImportFiles(ctx)
	if err != nil {
		t.Fatalf("readUploadedImportFiles returned error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files len = %d, want 2", len(files))
	}
	got := map[string]bool{}
	for _, file := range files {
		got[string(file.data)] = true
	}
	if !got["rt-1"] || !got["rt-2"] {
		t.Fatalf("files = %+v, want both uploaded files", files)
	}
}

func TestValidateImportFileSize(t *testing.T) {
	if err := validateImportFileSize(&multipart.FileHeader{Filename: "ok.txt", Size: importFileSizeLimitBytes}); err != nil {
		t.Fatalf("validateImportFileSize returned error for boundary size: %v", err)
	}

	err := validateImportFileSize(&multipart.FileHeader{Filename: "too-big.txt", Size: importFileSizeLimitBytes + 1})
	if err == nil {
		t.Fatal("expected oversized file error, got nil")
	}
	if got, want := err.Error(), "文件 too-big.txt 大小超过 20MB"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestImportAccountsJSONReturnsExistingNoTokenMessageForUnsupportedJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	req := newMultipartJSONRequest(t, "accounts.json", `{"accounts":[{"credentials":{}}]}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = req

	handler := &Handler{}
	handler.importAccountsJSON(ctx, "")

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}

	var payload map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := payload["error"]; got != "JSON 文件中未找到有效的 refresh_token 或 access_token" {
		t.Fatalf("error = %q, want %q", got, "JSON 文件中未找到有效的 refresh_token 或 access_token")
	}
}

func TestImportAccountsJSONRejectsInvalidJSONFile(t *testing.T) {
	gin.SetMode(gin.TestMode)

	req := newMultipartJSONRequest(t, "broken.json", `{"accounts":[}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = req

	handler := &Handler{}
	handler.importAccountsJSON(ctx, "")

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}

	var payload map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := payload["error"]; got != "文件 broken.json 不是有效的 JSON 格式" {
		t.Fatalf("error = %q, want %q", got, "文件 broken.json 不是有效的 JSON 格式")
	}
}

func TestImportAccountsCommonTriggersUsageProbeForImportedAccountWithAccessToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newTestAdminDB(t)
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	probed := make(chan int64, 1)
	handler := &Handler{
		db:    db,
		store: store,
		probeUsage: func(_ context.Context, acc *auth.Account) error {
			probed <- acc.DBID
			return nil
		},
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/accounts/import", nil)

	handler.importAccountsCommon(ctx, []importToken{{
		refreshToken: "rt-import-probe",
		accessToken:  "at-import-probe",
	}}, "")

	select {
	case id := <-probed:
		if id == 0 {
			t.Fatal("probed account id is zero")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("usage probe was not triggered for imported account with access token")
	}
}

func TestImportAccountsCommonKeepsSub2APISharedAccountDifferentUsers(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newTestAdminDB(t)
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4", LazyMode: true})
	store.SetLazyMode(true)
	handler := &Handler{db: db, store: store}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/accounts/import", nil)

	handler.importAccountsCommon(ctx, []importToken{
		{
			accessToken:      "at-sub2api-one",
			name:             "Team User One",
			email:            "one@example.com",
			chatgptAccountID: "team-workspace",
			userID:           "user-one",
		},
		{
			accessToken:      "at-sub2api-two",
			name:             "Team User Two",
			email:            "two@example.com",
			chatgptAccountID: "team-workspace",
			userID:           "user-two",
		},
	}, "")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	payload := recorder.Body.String()
	if !strings.Contains(payload, `"type":"complete"`) || !strings.Contains(payload, `"success":2`) || !strings.Contains(payload, `"duplicate":0`) {
		t.Fatalf("SSE payload = %q, want complete success=2 duplicate=0", payload)
	}

	existingATs, err := db.GetAllAccessTokens(context.Background())
	if err != nil {
		t.Fatalf("GetAllAccessTokens: %v", err)
	}
	if !existingATs["at-sub2api-one"] || !existingATs["at-sub2api-two"] {
		t.Fatalf("existing ATs = %#v, want both imported", existingATs)
	}
}

func TestImportAccountsCommonDedupesSameAccessTokenDifferentFineIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newTestAdminDB(t)
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4", LazyMode: true})
	store.SetLazyMode(true)
	handler := &Handler{db: db, store: store}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/accounts/import", nil)

	handler.importAccountsCommon(ctx, []importToken{
		{
			accessToken:      "at-same-hard-duplicate",
			name:             "Same AT User One",
			email:            "one@example.com",
			chatgptAccountID: "team-workspace",
			userID:           "user-one",
		},
		{
			accessToken:      "at-same-hard-duplicate",
			name:             "Same AT User Two",
			email:            "two@example.com",
			chatgptAccountID: "team-workspace",
			userID:           "user-two",
		},
	}, "")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	payload := recorder.Body.String()
	if !strings.Contains(payload, `"type":"complete"`) || !strings.Contains(payload, `"success":1`) || !strings.Contains(payload, `"total":1`) {
		t.Fatalf("SSE payload = %q, want complete success=1 total=1", payload)
	}
}

func TestImportAccountsCommonKeepsAccountIDDifferentUsers(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newTestAdminDB(t)
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4", LazyMode: true})
	store.SetLazyMode(true)
	handler := &Handler{db: db, store: store}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/accounts/import", nil)

	handler.importAccountsCommon(ctx, []importToken{
		{
			accessToken: "at-account-id-one",
			name:        "Account ID User One",
			email:       "one@example.com",
			accountID:   "legacy-workspace",
			userID:      "user-one",
		},
		{
			accessToken: "at-account-id-two",
			name:        "Account ID User Two",
			email:       "two@example.com",
			accountID:   "legacy-workspace",
			userID:      "user-two",
		},
	}, "")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	payload := recorder.Body.String()
	if !strings.Contains(payload, `"type":"complete"`) || !strings.Contains(payload, `"success":2`) || !strings.Contains(payload, `"duplicate":0`) {
		t.Fatalf("SSE payload = %q, want complete success=2 duplicate=0", payload)
	}
}

func TestImportAccountsCommonSkipsExistingScopedUserIDWithChangedAccessToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newTestAdminDB(t)
	if _, err := db.InsertAccountWithCredentials(context.Background(), "existing-user", map[string]interface{}{
		"chatgpt_account_id": "team-workspace",
		"chatgpt_user_id":    "same-user",
		"access_token":       "at-existing-user",
	}, ""); err != nil {
		t.Fatalf("InsertAccountWithCredentials: %v", err)
	}

	handler := &Handler{db: db}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/accounts/import", nil)

	handler.importAccountsCommon(ctx, []importToken{{
		accessToken:      "at-new-user",
		name:             "Existing User Duplicate",
		chatgptAccountID: "team-workspace",
		userID:           "same-user",
	}}, "")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := payload["success"]; got != float64(0) {
		t.Fatalf("success = %v, want 0", got)
	}
	if got := payload["duplicate"]; got != float64(1) {
		t.Fatalf("duplicate = %v, want 1", got)
	}
}

func TestImportAccountsCommonSkipsExistingScopedEmailWithChangedAccessToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newTestAdminDB(t)
	if _, err := db.InsertAccountWithCredentials(context.Background(), "existing-email", map[string]interface{}{
		"account_id":   "team-workspace",
		"email":        "Same@Example.com",
		"access_token": "at-existing-email",
	}, ""); err != nil {
		t.Fatalf("InsertAccountWithCredentials: %v", err)
	}

	handler := &Handler{db: db}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/accounts/import", nil)

	handler.importAccountsCommon(ctx, []importToken{{
		accessToken: "at-new-email",
		name:        "Existing Email Duplicate",
		accountID:   "team-workspace",
		email:       "same@example.com",
	}}, "")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := payload["success"]; got != float64(0) {
		t.Fatalf("success = %v, want 0", got)
	}
	if got := payload["duplicate"]; got != float64(1) {
		t.Fatalf("duplicate = %v, want 1", got)
	}
}

func TestImportAccountsCommonSavesUserIDCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newTestAdminDB(t)
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4", LazyMode: true})
	store.SetLazyMode(true)
	handler := &Handler{db: db, store: store}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/accounts/import", nil)

	handler.importAccountsCommon(ctx, []importToken{{
		accessToken:      "at-save-user-id",
		name:             "Save User ID",
		chatgptAccountID: "team-workspace",
		userID:           "saved-user-id",
		email:            "saved@example.com",
	}}, "")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	rows, err := db.ListActive(context.Background())
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListActive returned %d rows, want 1", len(rows))
	}
	if got := rows[0].GetCredential("user_id"); got != "saved-user-id" {
		t.Fatalf("user_id credential = %q, want saved-user-id", got)
	}
	if got := rows[0].GetCredential("chatgpt_user_id"); got != "saved-user-id" {
		t.Fatalf("chatgpt_user_id credential = %q, want saved-user-id", got)
	}
}

func TestImportAccountsCommonDedupesSharedChatGPTAccountWithoutFineIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newTestAdminDB(t)
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4", LazyMode: true})
	store.SetLazyMode(true)
	handler := &Handler{db: db, store: store}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/accounts/import", nil)

	handler.importAccountsCommon(ctx, []importToken{
		{refreshToken: "rt-no-fine-1", name: "No Fine One", chatgptAccountID: "same-workspace"},
		{refreshToken: "rt-no-fine-2", name: "No Fine Two", chatgptAccountID: "same-workspace"},
	}, "")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	payload := recorder.Body.String()
	if !strings.Contains(payload, `"type":"complete"`) || !strings.Contains(payload, `"success":1`) {
		t.Fatalf("SSE payload = %q, want complete success=1", payload)
	}
	if !strings.Contains(payload, `"total":1`) {
		t.Fatalf("SSE payload = %q, want file-level duplicate collapsed to total=1", payload)
	}
}

func TestImportAccountsCommonSkipsExistingChatGPTAccountWithoutFineIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newTestAdminDB(t)
	existingID, err := db.InsertAccount(context.Background(), "existing-workspace", "rt-existing-workspace", "")
	if err != nil {
		t.Fatalf("InsertAccount: %v", err)
	}
	if err := db.UpdateCredentials(context.Background(), existingID, map[string]interface{}{
		"chatgpt_account_id": "existing-workspace",
	}); err != nil {
		t.Fatalf("UpdateCredentials: %v", err)
	}

	handler := &Handler{db: db}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/accounts/import", nil)

	handler.importAccountsCommon(ctx, []importToken{{
		refreshToken:     "rt-new-workspace",
		name:             "Existing Workspace Duplicate",
		chatgptAccountID: "existing-workspace",
	}}, "")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := payload["success"]; got != float64(0) {
		t.Fatalf("success = %v, want 0", got)
	}
	if got := payload["duplicate"]; got != float64(1) {
		t.Fatalf("duplicate = %v, want 1", got)
	}
	if got := payload["total"]; got != float64(1) {
		t.Fatalf("total = %v, want 1", got)
	}
}

func TestImportAccountsCommonRefreshesAndProbesRTOnlyImport(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newTestAdminDB(t)
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	probed := make(chan int64, 1)
	handler := &Handler{
		db:    db,
		store: store,
		refreshAccount: func(_ context.Context, id int64) error {
			acc := store.FindByID(id)
			if acc == nil {
				return fmt.Errorf("account %d not found", id)
			}
			acc.Mu().Lock()
			acc.AccessToken = "at-refreshed"
			acc.Mu().Unlock()
			return nil
		},
		probeUsage: func(_ context.Context, acc *auth.Account) error {
			probed <- acc.DBID
			return nil
		},
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/accounts/import", nil)

	handler.importAccountsCommon(ctx, []importToken{{refreshToken: "rt-import-refresh-probe"}}, "")

	select {
	case id := <-probed:
		if id == 0 {
			t.Fatal("probed account id is zero")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("usage probe was not triggered after RT-only import refresh")
	}
}

func TestAddAccountStreamReportsProgressAndProbesAfterRefresh(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newTestAdminDB(t)
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	probed := make(chan int64, 2)
	handler := &Handler{
		db:    db,
		store: store,
		refreshAccount: func(_ context.Context, id int64) error {
			acc := store.FindByID(id)
			if acc == nil {
				return fmt.Errorf("account %d not found", id)
			}
			acc.Mu().Lock()
			acc.AccessToken = fmt.Sprintf("at-%d", id)
			acc.Mu().Unlock()
			return nil
		},
		probeUsage: func(_ context.Context, acc *auth.Account) error {
			probed <- acc.DBID
			return nil
		},
	}

	body := bytes.NewBufferString(`{"refresh_token":"rt-stream-1\nrt-stream-2"}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/accounts?stream=true", body)
	ctx.Request.Header.Set("Content-Type", "application/json")

	handler.AddAccount(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	payload := recorder.Body.String()
	if !strings.Contains(payload, `"type":"complete"`) || !strings.Contains(payload, `"success":2`) {
		t.Fatalf("SSE payload = %q, want complete success=2", payload)
	}

	seen := map[int64]bool{}
	deadline := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case id := <-probed:
			seen[id] = true
		case <-deadline:
			t.Fatalf("usage probes = %v, want 2 accounts probed", seen)
		}
	}
}

func newMultipartJSONRequest(t *testing.T, filename string, content string) *http.Request {
	t.Helper()

	return newMultipartRequest(t, map[string]string{filename: content})
}

func newMultipartRequest(t *testing.T, files map[string]string) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for filename, content := range files {
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatalf("part.Write: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/admin/accounts/import", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}
