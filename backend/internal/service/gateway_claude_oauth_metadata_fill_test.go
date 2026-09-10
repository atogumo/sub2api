package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestFillEmptyMetadataAccountUUID(t *testing.T) {
	const device = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const session = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const accountUUID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

	oauth := &Account{Type: AccountTypeOAuth, Platform: PlatformAnthropic, Extra: map[string]any{"account_uuid": accountUUID}}
	body := func(userID string) ([]byte, *ParsedRequest) {
		b := []byte(`{"model":"claude-sonnet-5","max_tokens":64,"metadata":{"user_id":` + jsonQuote(userID) + `},"messages":[{"role":"user","content":"quota"}]}`)
		return b, &ParsedRequest{MetadataUserID: userID}
	}

	t.Run("json format with empty account is filled, other fields untouched", func(t *testing.T) {
		raw := `{"device_id":"` + device + `","account_uuid":"","session_id":"` + session + `"}`
		b, parsed := body(raw)
		out, changed := fillEmptyMetadataAccountUUID(b, parsed, oauth)
		require.True(t, changed)
		got := ParseMetadataUserID(gjson.GetBytes(out, "metadata.user_id").String())
		require.NotNil(t, got)
		require.True(t, got.IsNewFormat)
		require.Equal(t, device, got.DeviceID)
		require.Equal(t, accountUUID, got.AccountUUID)
		require.Equal(t, session, got.SessionID)
		// Nothing else in the body moves.
		require.Equal(t, "claude-sonnet-5", gjson.GetBytes(out, "model").String())
		require.Equal(t, int64(64), gjson.GetBytes(out, "max_tokens").Int())
	})

	t.Run("legacy format with empty account is filled in legacy format", func(t *testing.T) {
		raw := "user_" + device + "_account__session_" + session
		b, parsed := body(raw)
		out, changed := fillEmptyMetadataAccountUUID(b, parsed, oauth)
		require.True(t, changed)
		require.Equal(t, "user_"+device+"_account_"+accountUUID+"_session_"+session, gjson.GetBytes(out, "metadata.user_id").String())
	})

	t.Run("non-empty account segment is left alone", func(t *testing.T) {
		raw := `{"device_id":"` + device + `","account_uuid":"cccccccc-cccc-4ccc-8ccc-cccccccccccc","session_id":"` + session + `"}`
		b, parsed := body(raw)
		out, changed := fillEmptyMetadataAccountUUID(b, parsed, oauth)
		require.False(t, changed)
		require.Equal(t, string(b), string(out))
	})

	t.Run("no metadata, non-oauth account, or account without uuid: unchanged", func(t *testing.T) {
		raw := `{"device_id":"` + device + `","account_uuid":"","session_id":"` + session + `"}`
		b, parsed := body(raw)

		_, changed := fillEmptyMetadataAccountUUID(b, &ParsedRequest{MetadataUserID: ""}, oauth)
		require.False(t, changed)

		apiKey := &Account{Type: AccountTypeAPIKey, Platform: PlatformAnthropic, Extra: map[string]any{"account_uuid": accountUUID}}
		_, changed = fillEmptyMetadataAccountUUID(b, parsed, apiKey)
		require.False(t, changed)

		noUUID := &Account{Type: AccountTypeOAuth, Platform: PlatformAnthropic}
		_, changed = fillEmptyMetadataAccountUUID(b, parsed, noUUID)
		require.False(t, changed)
	})
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
