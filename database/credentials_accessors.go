package database

import (
	"fmt"
	"strconv"
	"strings"
)

func (a *AccountRow) GetCredentialFloat64(key string) (float64, bool) {
	if a == nil || a.Credentials == nil {
		return 0, false
	}
	v, ok := a.Credentials[key]
	if !ok || v == nil {
		return 0, false
	}
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case string:
		parsed, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func (a *AccountRow) GetCredentialBool(key string) bool {
	if a == nil || a.Credentials == nil {
		return false
	}
	v, ok := a.Credentials[key]
	if !ok || v == nil {
		return false
	}
	switch val := v.(type) {
	case bool:
		return val
	case string:
		parsed, err := strconv.ParseBool(val)
		return err == nil && parsed
	default:
		return false
	}
}

func normalizeAccountIdentityScope(credentials map[string]interface{}) string {
	return firstCredentialString(credentials, "chatgpt_account_id", "account_id")
}

func normalizeAccountIdentityUser(credentials map[string]interface{}) string {
	return firstCredentialString(credentials, "chatgpt_user_id", "user_id", "chatgpt_account_user_id", "account_user_id", "accountUserID")
}

func normalizeAccountIdentityEmail(credentials map[string]interface{}) string {
	return strings.ToLower(firstCredentialString(credentials, "email"))
}

func accountScopedIdentityKey(scope, value string) string {
	scope = strings.TrimSpace(scope)
	value = strings.TrimSpace(value)
	if scope == "" || value == "" {
		return ""
	}
	return scope + "\x00" + value
}

func firstCredentialString(credentials map[string]interface{}, keys ...string) string {
	if credentials == nil {
		return ""
	}
	for _, key := range keys {
		value, ok := credentials[key]
		if !ok || value == nil {
			continue
		}
		s := strings.TrimSpace(credentialValueString(value))
		if s != "" {
			return s
		}
	}
	return ""
}

func credentialValueString(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case int:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

func (a *AccountRow) GetCredentialStringMap(key string) map[string]string {
	if a == nil || a.Credentials == nil {
		return nil
	}
	v, ok := a.Credentials[key]
	if !ok || v == nil {
		return nil
	}
	switch val := v.(type) {
	case map[string]string:
		return cloneTrimmedStringMap(val)
	case map[string]interface{}:
		out := make(map[string]string, len(val))
		for key, raw := range val {
			name := strings.TrimSpace(key)
			if name == "" || raw == nil {
				continue
			}
			out[name] = strings.TrimSpace(fmt.Sprint(raw))
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

func cloneTrimmedStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		name := strings.TrimSpace(key)
		if name == "" {
			continue
		}
		out[name] = strings.TrimSpace(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
