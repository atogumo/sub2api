//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// A non-Claude-Code client on /v1/messages (or count_tokens) in a claude_code_only
// group without fallback used to fail account selection and go out as 503
// "Service temporarily unavailable": retried by downstream gateways and SDKs and
// read by users as an outage. It must be a 403 permission_error that names the
// restriction, with the validator's detail kept off the wire.
func TestGatewayAnthropicHandlersClaudeCodeOnlyRejection(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		primaryGroupID    = int64(9400)
		fallbackGroupID   = int64(9401)
		primaryAccountID  = int64(9410)
		fallbackAccountID = int64(9411)
	)

	newAccount := func(id, groupID int64) *service.Account {
		return &service.Account{
			ID: id, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Concurrency: 1,
			AccountGroups: []service.AccountGroup{{AccountID: id, GroupID: groupID}},
		}
	}

	endpoints := []struct {
		name string
		path string
		body string
		call func(*GatewayHandler, *gin.Context)
	}{
		{
			name: "messages", path: "/v1/messages",
			body: `{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"stream":true}`,
			call: (*GatewayHandler).Messages,
		},
		{
			name: "count tokens", path: "/v1/messages/count_tokens",
			body: `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`,
			call: (*GatewayHandler).CountTokens,
		},
	}

	for _, ep := range endpoints {
		for _, tc := range []struct {
			name        string
			hasFallback bool
		}{
			{name: "without fallback group", hasFallback: false},
			{name: "with fallback group", hasFallback: true},
		} {
			t.Run(ep.name+"/"+tc.name, func(t *testing.T) {
				primary := &service.Group{
					ID: primaryGroupID, Hydrated: true, Platform: service.PlatformAnthropic,
					Status: service.StatusActive, ClaudeCodeOnly: true,
				}
				if tc.hasFallback {
					fallbackID := fallbackGroupID
					primary.FallbackGroupID = &fallbackID
				}
				fallback := &service.Group{
					ID: fallbackGroupID, Hydrated: true, Platform: service.PlatformAnthropic,
					Status: service.StatusActive,
				}

				schedulerCache := &groupScopedSchedulerCache{fakeSchedulerCache: &fakeSchedulerCache{accounts: []*service.Account{
					newAccount(primaryAccountID, primaryGroupID),
					newAccount(fallbackAccountID, fallbackGroupID),
				}}}
				gatewayService := service.NewGatewayService(
					nil, &groupMapRepo{fakeGroupRepo: &fakeGroupRepo{}, groups: map[int64]*service.Group{
						primaryGroupID:  primary,
						fallbackGroupID: fallback,
					}}, nil, nil, nil, nil, nil, nil, nil,
					service.NewSchedulerSnapshotService(schedulerCache, nil, nil, nil, nil),
					nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
				)
				cfg := &config.Config{RunMode: config.RunModeSimple}
				billingCacheService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
				t.Cleanup(billingCacheService.Stop)
				h := &GatewayHandler{
					gatewayService:      gatewayService,
					billingCacheService: billingCacheService,
					concurrencyHelper:   NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0),
					maxAccountSwitches:  1,
					cfg:                 cfg,
				}

				primaryGroupIDRef := primaryGroupID
				apiKey := &service.APIKey{
					ID: 9420, UserID: 9430, GroupID: &primaryGroupIDRef, Group: primary, Status: service.StatusActive,
					User: &service.User{ID: 9430, Concurrency: 10, Balance: 100},
				}
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				ctx := context.WithValue(context.Background(), ctxkey.Group, primary)
				req := httptest.NewRequest(http.MethodPost, ep.path, bytes.NewBufferString(ep.body)).WithContext(ctx)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("User-Agent", "Go-http-client/1.1")
				c.Request = req
				c.Set(string(middleware.ContextKeyAPIKey), apiKey)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})

				ep.call(h, c)

				if tc.hasFallback {
					// The fallback group still serves the request; nothing about the
					// restriction may reach the client.
					require.NotContains(t, recorder.Body.String(), "restricted to Claude Code clients")
					selected, reachedSelection := c.Get(opsAccountIDKey)
					require.True(t, reachedSelection, "a Claude Code only group with fallback must reach account selection")
					require.Equal(t, fallbackAccountID, selected)
					return
				}

				require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
				var body struct {
					Error struct {
						Type    string `json:"type"`
						Message string `json:"message"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body), recorder.Body.String())
				require.Equal(t, "permission_error", body.Error.Type)
				require.Equal(t, claudeCodeOnlyRejectionMessage, body.Error.Message)

				// Validator detail and pool wording stay off the wire.
				wire := strings.ToLower(recorder.Body.String())
				for _, leak := range []string{"client check failed", "ua_mismatch", "go-http-client", "account", "temporarily unavailable"} {
					require.NotContains(t, wire, leak)
				}

				// Operators still see why the request was refused, attributed to local
				// policy rather than to the account pool.
				reason, ok := c.Get(service.OpsUpstreamErrorMessageKey)
				require.True(t, ok, "the rejection detail must be recorded for ops")
				require.Contains(t, reason.(string), "client check failed")
				require.False(t, isOpsNoAvailableAccountMessage(reason.(string)), "a policy rejection must not be filed under the pool")
				require.Equal(t, service.OpsClientBusinessLimitedReasonLocalPolicyDenied, service.OpsClientBusinessLimitedReason(c))
				require.False(t, isOpsRoutingCapacityLimited(c))
				_, selected := c.Get(opsAccountIDKey)
				require.False(t, selected, "no account may be selected for a rejected client")
			})
		}
	}
}

func TestClassifyClaudeCodeOnlyRejection_IgnoresOtherSelectionErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	_, ok := classifyClaudeCodeOnlyRejection(c, service.ErrNoAvailableAccounts)
	require.False(t, ok)
	_, ok = classifyClaudeCodeOnlyRejection(c, nil)
	require.False(t, ok)
	require.False(t, service.HasOpsClientBusinessLimited(c), "non-matching errors must leave ops attribution untouched")
	_, recorded := c.Get(service.OpsUpstreamErrorMessageKey)
	require.False(t, recorded)
}
