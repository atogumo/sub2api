package service

import (
	"fmt"
	"strconv"
	"strings"
)

// claudeCodeSystemBlockHeadMinLen 只对模板量级的 system 块输出开头片段：分类器提示词
// 和 Claude Code 主提示词都远超此长度，而待判定动作、会话上下文等短块可能含用户内容，
// 一律只记长度。
const claudeCodeSystemBlockHeadMinLen = 2000

// DescribeClaudeCodeSystemBlocks 把请求的 system 字段压成一行形态摘要，供 claude_code_only
// 拒绝归因：每个文本块的长度、是否命中分类器前缀、缺失了哪些分类器标记，模板量级的块
// 再附 60 字开头。只进运维日志，不进对外响应。
func DescribeClaudeCodeSystemBlocks(system any) string {
	switch v := system.(type) {
	case nil:
		return "absent"
	case string:
		return "string" + describeClaudeCodeSystemText(v)
	case []any:
		parts := make([]string, 0, len(v))
		for i, raw := range v {
			entry, ok := raw.(map[string]any)
			if !ok {
				parts = append(parts, strconv.Itoa(i)+":non-object")
				continue
			}
			text, ok := entry["text"].(string)
			if !ok {
				t, _ := entry["type"].(string)
				parts = append(parts, strconv.Itoa(i)+":type="+t)
				continue
			}
			parts = append(parts, strconv.Itoa(i)+describeClaudeCodeSystemText(text))
		}
		return "[" + strings.Join(parts, " ") + "]"
	default:
		return fmt.Sprintf("%T", system)
	}
}

func describeClaudeCodeSystemText(text string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "{len=%d", len(text))
	if strings.HasPrefix(text, claudeCodeSecurityMonitorPromptPrefix) {
		_, _ = b.WriteString(" monitor_prefix=yes")
		missing := make([]string, 0, len(claudeCodeSecurityMonitorMarkers))
		for _, marker := range claudeCodeSecurityMonitorMarkers {
			if !strings.Contains(text, marker) {
				missing = append(missing, marker)
			}
		}
		fmt.Fprintf(&b, " missing_markers=%q", missing)
	} else {
		_, _ = b.WriteString(" monitor_prefix=no")
	}
	if len(text) >= claudeCodeSystemBlockHeadMinLen {
		head := text
		if len(head) > 60 {
			head = head[:60]
		}
		fmt.Fprintf(&b, " head=%q", head)
	}
	_ = b.WriteByte('}')
	return b.String()
}
