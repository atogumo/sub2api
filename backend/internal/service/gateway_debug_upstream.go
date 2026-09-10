package service

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
)

// claudeCodeUpstreamDebugEnabled 开启上游请求/响应形态诊断日志（实验排障用，默认关闭）。
// 环境变量 CLAUDE_CODE_DEBUG_UPSTREAM=true。只记录形态字段与响应头，不记录消息正文、
// 不记录凭据（authorization / x-api-key / cookie 一律脱敏）。
var claudeCodeUpstreamDebugEnabled = strings.EqualFold(strings.TrimSpace(os.Getenv("CLAUDE_CODE_DEBUG_UPSTREAM")), "true")

// shouldLogUpstreamDebug 决定哪些往返值得记录：上游 4xx/5xx，以及非流式请求
// （Claude Code 的主对话是流式的，非流式基本都是探测、分类器等辅助请求）。
func shouldLogUpstreamDebug(body []byte, resp *http.Response) bool {
	if !claudeCodeUpstreamDebugEnabled || resp == nil {
		return false
	}
	if resp.StatusCode >= 400 {
		return true
	}
	return !gjson.GetBytes(body, "stream").Bool()
}

// logUpstreamDebug 输出一条上游往返的形态摘要。
func logUpstreamDebug(account *Account, req *http.Request, body []byte, resp *http.Response) {
	if account == nil || req == nil || resp == nil {
		return
	}
	logger.LegacyPrintf("service.gateway",
		"[DEBUG-UPSTREAM] account=%d status=%d url=%s\n  req_headers=%s\n  body=%s\n  resp_headers=%s",
		account.ID, resp.StatusCode, safeUpstreamURL(req.URL.String()),
		formatHeadersForDebug(req.Header), summarizeAnthropicBodyForDebug(body), formatHeadersForDebug(resp.Header))
}

func formatHeadersForDebug(h http.Header) string {
	if h == nil {
		return "{}"
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	_ = b.WriteByte('{')
	for i, k := range keys {
		lk := strings.ToLower(k)
		v := strings.Join(h[k], ", ")
		switch lk {
		case "authorization", "x-api-key", "cookie", "set-cookie":
			v = "<redacted>"
		}
		if i > 0 {
			_, _ = b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s=%q", lk, v)
	}
	_ = b.WriteByte('}')
	return b.String()
}

// summarizeAnthropicBodyForDebug 只取形态字段：模型、流式、max_tokens、采样参数、thinking、
// stop_sequences、metadata.user_id、system 各块的长度与前 80 字、messages 数量与角色、
// tools 数量、cache_control 出现次数。不输出消息正文。
func summarizeAnthropicBodyForDebug(body []byte) string {
	if len(body) == 0 {
		return "{}"
	}
	g := func(path string) string {
		r := gjson.GetBytes(body, path)
		if !r.Exists() {
			return "-"
		}
		return r.Raw
	}
	var b strings.Builder
	fmt.Fprintf(&b, "{model=%s stream=%s max_tokens=%s temperature=%s top_p=%s thinking=%s stop_sequences=%s output_config=%s metadata.user_id=%s",
		g("model"), g("stream"), g("max_tokens"), g("temperature"), g("top_p"), g("thinking"), g("stop_sequences"), g("output_config"), g("metadata.user_id"))

	sys := gjson.GetBytes(body, "system")
	switch {
	case !sys.Exists():
		_, _ = b.WriteString(" system=absent")
	case sys.Type == gjson.String:
		fmt.Fprintf(&b, " system=string(len=%d,head=%q)", len(sys.String()), head(sys.String(), 80))
	case sys.IsArray():
		fmt.Fprintf(&b, " system=array(n=%d)[", len(sys.Array()))
		for i, e := range sys.Array() {
			if i > 0 {
				_, _ = b.WriteString(" | ")
			}
			t := e.Get("text").String()
			fmt.Fprintf(&b, "%d:len=%d cache_control=%s head=%q", i, len(t), rawOrDash(e.Get("cache_control")), head(t, 80))
		}
		_, _ = b.WriteString("]")
	default:
		fmt.Fprintf(&b, " system=%s", sys.Type)
	}

	msgs := gjson.GetBytes(body, "messages")
	roles := make([]string, 0, 8)
	for _, m := range msgs.Array() {
		roles = append(roles, m.Get("role").String())
		if len(roles) >= 8 {
			break
		}
	}
	fmt.Fprintf(&b, " messages=%d roles=%v tools=%d cache_control_count=%d bytes=%d}",
		len(msgs.Array()), roles, len(gjson.GetBytes(body, "tools").Array()), strings.Count(string(body), `"cache_control"`), len(body))
	return b.String()
}

func rawOrDash(r gjson.Result) string {
	if !r.Exists() {
		return "-"
	}
	return r.Raw
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
