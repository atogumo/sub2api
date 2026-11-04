package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// gateway.cancel_upstream_on_client_disconnect: an Anthropic /v1/messages stream
// follows the client. When the client leaves, the upstream request is cancelled
// (generation stops) and the usage observed so far goes out as a normal result.

// blockingUpstreamStream sends its payload, then blocks like a model that is still
// thinking, until the upstream request's context is cancelled.
type blockingUpstreamStream struct {
	ctx     context.Context
	payload []byte
	sent    bool
	waiting chan struct{}
	once    sync.Once
}

func (s *blockingUpstreamStream) Read(p []byte) (int, error) {
	if !s.sent {
		s.sent = true
		return copy(p, s.payload), nil
	}
	s.once.Do(func() { close(s.waiting) })
	<-s.ctx.Done()
	return 0, s.ctx.Err()
}

func (s *blockingUpstreamStream) Close() error { return nil }

type blockingAnthropicUpstream struct {
	payload []byte
	waiting chan struct{}
	mu      sync.Mutex
	reqCtx  context.Context
}

func (u *blockingAnthropicUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.reqCtx = req.Context()
	u.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"X-Request-Id": []string{"rid-cancel"},
		},
		Body: &blockingUpstreamStream{ctx: req.Context(), payload: u.payload, waiting: u.waiting},
	}, nil
}

func (u *blockingAnthropicUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func (u *blockingAnthropicUpstream) upstreamContext() context.Context {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.reqCtx
}

const thinkingStreamStart = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":7548,"cache_creation_input_tokens":36403,"output_tokens":4}}}` +
	"\n\n"

func newCancelOnDisconnectService(upstream HTTPUpstream, cancelOnDisconnect bool) *GatewayService {
	cfg := &config.Config{Gateway: config.GatewayConfig{
		MaxLineSize:                      defaultMaxLineSize,
		CancelUpstreamOnClientDisconnect: cancelOnDisconnect,
	}}
	return &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}
}

func TestGatewayService_Forward_CancelUpstreamOnClientDisconnect(t *testing.T) {
	for _, tc := range []struct {
		name    string
		account func() *Account
		parsed  func(t *testing.T) *ParsedRequest
	}{
		{
			name:    "oauth",
			account: newAnthropicOAuthAccountForPartialUsageTest,
			parsed: func(t *testing.T) *ParsedRequest {
				body := []byte(`{"model":"claude-opus-5-5","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
				parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
				require.NoError(t, err)
				return parsed
			},
		},
		{
			name:    "api key passthrough",
			account: newAnthropicAPIKeyAccountForTest,
			parsed: func(t *testing.T) *ParsedRequest {
				body := []byte(`{"model":"claude-opus-5-5","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
				return &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-5-5", Stream: true}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			clientCtx, clientLeaves := context.WithCancel(context.Background())
			defer clientLeaves()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(clientCtx)

			upstream := &blockingAnthropicUpstream{payload: []byte(thinkingStreamStart), waiting: make(chan struct{})}
			svc := newCancelOnDisconnectService(upstream, true)

			go func() {
				// The model is mid-generation when the client presses Esc.
				<-upstream.waiting
				clientLeaves()
			}()

			done := make(chan struct{})
			var result *ForwardResult
			var err error
			go func() {
				defer close(done)
				result, err = svc.Forward(clientCtx, c, tc.account(), tc.parsed(t))
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the upstream stream kept running after the client left")
			}

			require.NoError(t, err, "a client that leaves is a served request, not a forward failure")
			require.NotNil(t, result)
			require.True(t, result.ClientDisconnect)
			require.True(t, result.Stream)
			require.Equal(t, 7548, result.Usage.InputTokens)
			require.Equal(t, 36403, result.Usage.CacheCreationInputTokens)
			// Output is only counted from message_delta; the placeholder in message_start
			// is ignored, as on the existing partial-usage path.
			require.Equal(t, 0, result.Usage.OutputTokens, "only the usage observed up to the disconnect is recorded")
			require.Equal(t, "rid-cancel", result.RequestID)
			require.ErrorIs(t, upstream.upstreamContext().Err(), context.Canceled, "the upstream request must be cancelled with the client")
		})
	}
}

func TestGatewayService_AnthropicStreamUpstreamContext(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("default drains: a stream is detached from the client", func(t *testing.T) {
		svc := newCancelOnDisconnectService(nil, false)
		ctx, release := svc.anthropicStreamUpstreamContext(parent, true)
		defer release()
		require.NoError(t, ctx.Err())

		nonStream, releaseNonStream := svc.anthropicStreamUpstreamContext(parent, false)
		defer releaseNonStream()
		require.ErrorIs(t, nonStream.Err(), context.Canceled, "non-streaming requests always follow the client")
	})

	t.Run("enabled: a stream follows the client", func(t *testing.T) {
		svc := newCancelOnDisconnectService(nil, true)
		ctx, release := svc.anthropicStreamUpstreamContext(parent, true)
		defer release()
		require.ErrorIs(t, ctx.Err(), context.Canceled)
	})

	t.Run("nil service and config keep the default", func(t *testing.T) {
		var nilSvc *GatewayService
		ctx, release := nilSvc.anthropicStreamUpstreamContext(parent, true)
		defer release()
		require.NoError(t, ctx.Err())
		require.False(t, (&GatewayService{}).cancelUpstreamOnClientDisconnect())
	})
}

func TestGatewayService_ClientCancelledStream(t *testing.T) {
	observed := &streamingResult{usage: &ClaudeUsage{InputTokens: 10}, clientDisconnect: true}
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := context.Canceled

	on := newCancelOnDisconnectService(nil, true)
	off := newCancelOnDisconnectService(nil, false)

	require.True(t, on.clientCancelledStream(context.Background(), observed, cancelled))
	require.True(t, on.clientCancelledStream(gone, observed, errStreamReadForTest), "a read error after the client left counts as the client's cancellation")

	require.False(t, off.clientCancelledStream(gone, observed, cancelled), "disabled: keep the upstream behaviour")
	require.False(t, on.clientCancelledStream(gone, observed, nil))
	require.False(t, on.clientCancelledStream(gone, nil, cancelled))
	require.False(t, on.clientCancelledStream(gone, &streamingResult{usage: &ClaudeUsage{InputTokens: 10}}, cancelled), "the client is still there")
	require.False(t, on.clientCancelledStream(gone, &streamingResult{usage: &ClaudeUsage{}, clientDisconnect: true}, cancelled), "no observed usage: no zero-usage record")
	require.False(t, on.clientCancelledStream(context.Background(), observed, errStreamReadForTest), "an upstream failure while the client is connected stays an error")
}

var errStreamReadForTest = &streamReadErrorForTest{}

type streamReadErrorForTest struct{}

func (*streamReadErrorForTest) Error() string { return "stream read error: connection reset" }
