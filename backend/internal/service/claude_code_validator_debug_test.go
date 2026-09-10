package service

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDescribeClaudeCodeSystemBlocks(t *testing.T) {
	monitorPrompt, err := os.ReadFile("testdata/security_monitor_system_prompt.txt")
	require.NoError(t, err)

	require.Equal(t, "absent", DescribeClaudeCodeSystemBlocks(nil))

	// 真实 2.1.220 分类器提示词：前缀命中、六个标记齐全、附开头。
	got := DescribeClaudeCodeSystemBlocks([]any{
		map[string]any{"type": "text", "text": string(monitorPrompt)},
		map[string]any{"type": "text", "text": "Bash: mkdir demo_dir"},
	})
	require.Contains(t, got, "0{len="+strconv.Itoa(len(monitorPrompt))+" monitor_prefix=yes missing_markers=[]")
	require.Contains(t, got, `head="You are a security monitor for autonomous AI coding agents.`)
	// 短块只记长度与前缀结果，不带内容。
	require.Contains(t, got, "1{len=20 monitor_prefix=no}")
	require.NotContains(t, got, "mkdir")

	// 去掉一个标记后，缺失项被点名。
	tampered := strings.ReplaceAll(string(monitorPrompt), "## Output Format", "## Result")
	got = DescribeClaudeCodeSystemBlocks([]any{map[string]any{"type": "text", "text": tampered}})
	require.Contains(t, got, `missing_markers=["## Output Format"]`)

	// 非文本块与字符串 system 也有稳定形态。
	require.Equal(t, "[0:type=image]", DescribeClaudeCodeSystemBlocks([]any{map[string]any{"type": "image"}}))
	require.Equal(t, "string{len=5 monitor_prefix=no}", DescribeClaudeCodeSystemBlocks("hello"))
}
