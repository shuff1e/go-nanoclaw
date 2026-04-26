// Package log provides human-readable, educational logging for NanoClaw.
//
// Designed to help users understand agent interaction flow:
//
//	0 (default): Quiet — only errors
//	1 (-v):      Flow  — agent loop steps, routing, tool calls
//	2 (-vv):     Trace — full LLM HTTP request/response with formatted JSON
package log

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	verbosity atomic.Int32
	mu        sync.Mutex
	stepNum   atomic.Int32 // global step counter per request
)

// ANSI colors
const (
	reset   = "\033[0m"
	dim     = "\033[2m"
	bold    = "\033[1m"
	red     = "\033[31m"
	green   = "\033[32m"
	yellow  = "\033[33m"
	blue    = "\033[34m"
	magenta = "\033[35m"
	cyan    = "\033[36m"
	white   = "\033[37m"
)

// SetVerbosity sets the global verbosity level (0, 1, or 2).
func SetVerbosity(v int) {
	verbosity.Store(int32(v))
}

// Verbosity returns the current verbosity level.
func Verbosity() int {
	return int(verbosity.Load())
}

// ResetSteps resets the step counter (call at start of each user request).
func ResetSteps() {
	stepNum.Store(0)
}

func nextStep() int {
	return int(stepNum.Add(1))
}

func write(s string) {
	mu.Lock()
	defer mu.Unlock()
	fmt.Fprint(os.Stderr, s)
}

// ── Flow logging (-v) ───────────────────────────────────────

// Section prints a major section header.
//
//	=== Gateway.HandleInput ===  agent=main  input="hello world"
func Section(title string, kvs ...any) {
	if Verbosity() < 1 {
		return
	}
	line := fmt.Sprintf("\n%s=== %s ===%s", bold+cyan, title, reset)
	if len(kvs) > 0 {
		line += "  " + formatKVs(kvs...)
	}
	write(line + "\n")
}

// Step prints a numbered step in the agent flow.
//
//	[1] Routing message           source=main  matched_skills=1
func Step(label string, kvs ...any) {
	if Verbosity() < 1 {
		return
	}
	n := nextStep()
	line := fmt.Sprintf("  %s[%d]%s %s", dim, n, reset, label)
	if len(kvs) > 0 {
		line += dim + "  " + formatKVs(kvs...) + reset
	}
	write(line + "\n")
}

// SubStep prints a sub-step (indented further).
//
//	-> tool: run_command  args: {"command":"ls"}
func SubStep(label string, kvs ...any) {
	if Verbosity() < 1 {
		return
	}
	line := fmt.Sprintf("    %s->%s %s", yellow, reset, label)
	if len(kvs) > 0 {
		line += dim + "  " + formatKVs(kvs...) + reset
	}
	write(line + "\n")
}

// Result prints the outcome of a section.
//
//	<- done  turns=1  tool_rounds=2  response_len=156
func Result(label string, kvs ...any) {
	if Verbosity() < 1 {
		return
	}
	line := fmt.Sprintf("  %s<-%s %s%s%s", green, reset, green, label, reset)
	if len(kvs) > 0 {
		line += dim + "  " + formatKVs(kvs...) + reset
	}
	write(line + "\n")
}

// Warn prints a warning.
func Warn(msg string, kvs ...any) {
	if Verbosity() < 1 {
		return
	}
	line := fmt.Sprintf("  %s!! %s%s", yellow, msg, reset)
	if len(kvs) > 0 {
		line += dim + "  " + formatKVs(kvs...) + reset
	}
	write(line + "\n")
}

// ── Educational logging (-v) ────────────────────────────────

// Banner prints a box-drawn section header.
//
//	╭─── 📨 Gateway 收到请求 #1 ───────────────────────────────────
//	│ Agent: main | 输入: "你是谁啊"
//	╰──────────────────────────────────────────────────────────────
func Banner(emoji, title string, kvLines ...string) {
	if Verbosity() < 1 {
		return
	}
	const width = 64
	var sb strings.Builder

	// Top border
	header := fmt.Sprintf("─── %s %s ", emoji, title)
	pad := width - len([]rune(header)) - 1
	if pad < 3 {
		pad = 3
	}
	sb.WriteString(fmt.Sprintf("\n%s╭%s%s%s\n", cyan, header, strings.Repeat("─", pad), reset))

	// Content lines
	for _, line := range kvLines {
		sb.WriteString(fmt.Sprintf("%s│%s %s\n", cyan, reset, line))
	}

	// Bottom border
	sb.WriteString(fmt.Sprintf("%s╰%s%s\n", cyan, strings.Repeat("─", width-1), reset))

	write(sb.String())
}

// Narrative prints a numbered step with an educational annotation.
//
//	[1] 路由 (Router) — 判断消息该交给哪个Agent，匹配哪些Skill
func Narrative(label, explanation string) {
	if Verbosity() < 1 {
		return
	}
	n := nextStep()
	line := fmt.Sprintf("\n  %s[%d]%s %s%s%s %s— %s%s\n", dim, n, reset, bold+white, label, reset, dim, explanation, reset)
	write(line)
}

// Tree prints a tree-structured detail line.
//
//	├─ Provider: anthropic | Model: claude-opus-4.6
//	└─ 无工具调用 → 直接返回文本
//
// indent=0 is top-level (6 spaces). Each additional level adds "│   ".
// last=true uses └─, last=false uses ├─.
func Tree(indent int, last bool, line string) {
	if Verbosity() < 1 {
		return
	}
	branch := "├─"
	if last {
		branch = "└─"
	}

	prefix := "      " // 6 spaces base indent
	for i := 0; i < indent; i++ {
		prefix += "│   "
	}

	write(fmt.Sprintf("%s%s%s%s %s\n", prefix, yellow, branch, reset, line))
}

// Completion prints the final result of a section.
//
//	← 完成  turn=1  tool_rounds=0  response=256 chars
func Completion(label string, kvs ...any) {
	if Verbosity() < 1 {
		return
	}
	line := fmt.Sprintf("\n  %s←%s %s%s%s", green, reset, green, label, reset)
	if len(kvs) > 0 {
		line += "  " + dim + formatKVs(kvs...) + reset
	}
	write(line + "\n")
}

// ── Trace logging (-vv) ─────────────────────────────────────

// TraceHTTPRequest logs a formatted HTTP request.
func TraceHTTPRequest(method, url string, headers map[string]string, body string) {
	if Verbosity() < 2 {
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n  %s╭─── LLM Request ─────────────────────────────────────────────%s\n", blue, reset))
	sb.WriteString(fmt.Sprintf("  %s│%s %s%s %s%s\n", blue, reset, bold, method, url, reset))
	sb.WriteString(fmt.Sprintf("  %s│%s\n", blue, reset))

	// Headers
	sb.WriteString(fmt.Sprintf("  %s│%s %sHeaders:%s\n", blue, reset, dim, reset))
	for k, v := range headers {
		display := v
		if isSensitiveHeader(k) && len(display) > 12 {
			display = display[:6] + "****" + display[len(display)-4:]
		}
		sb.WriteString(fmt.Sprintf("  %s│%s   %s%s:%s %s\n", blue, reset, cyan, k, reset, display))
	}
	sb.WriteString(fmt.Sprintf("  %s│%s\n", blue, reset))

	// Body
	sb.WriteString(fmt.Sprintf("  %s│%s %sBody (%d bytes):%s\n", blue, reset, dim, len(body), reset))
	prettyBody := prettyJSON(body)
	for _, line := range strings.Split(prettyBody, "\n") {
		sb.WriteString(fmt.Sprintf("  %s│%s   %s\n", blue, reset, line))
	}
	sb.WriteString(fmt.Sprintf("  %s╰──────────────────────────────────────────────────────────────%s\n", blue, reset))

	write(sb.String())
}

// TraceHTTPResponse logs a formatted HTTP response.
func TraceHTTPResponse(statusCode int, body string) {
	if Verbosity() < 2 {
		return
	}

	statusColor := green
	if statusCode >= 400 {
		statusColor = red
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n  %s╭─── LLM Response ────────────────────────────────────────────%s\n", magenta, reset))
	sb.WriteString(fmt.Sprintf("  %s│%s Status: %s%d%s\n", magenta, reset, statusColor, statusCode, reset))
	sb.WriteString(fmt.Sprintf("  %s│%s\n", magenta, reset))
	sb.WriteString(fmt.Sprintf("  %s│%s %sBody (%d bytes):%s\n", magenta, reset, dim, len(body), reset))

	prettyBody := prettyJSON(body)
	for _, line := range strings.Split(prettyBody, "\n") {
		sb.WriteString(fmt.Sprintf("  %s│%s   %s\n", magenta, reset, line))
	}
	sb.WriteString(fmt.Sprintf("  %s╰──────────────────────────────────────────────────────────────%s\n", magenta, reset))

	write(sb.String())
}

// ── Compatibility wrappers (called from existing code) ──────

// Flow is called by modules that already use log.Flow().
// Maps to Step or SubStep depending on the step name.
func Flow(method, step string, args ...any) {
	// These are handled by direct Section/Step/SubStep calls now.
	// Keep as a fallback for any remaining callers.
	if Verbosity() < 1 {
		return
	}

	label := fmt.Sprintf("%s.%s", method, step)
	kvs := argsToKVs(args)

	switch {
	case strings.HasSuffix(step, "start") && !strings.Contains(method, "."):
		Section(method, kvs...)
	case step == "start":
		// Skip — sections handle these now
	default:
		SubStep(label, kvs...)
	}
}

// Trace is a raw trace log.
func Trace(msg string, args ...any) {
	if Verbosity() < 2 {
		return
	}
	write(fmt.Sprintf("  %s[TRACE] %s%s\n", dim, msg, reset))
}

// Info prints an info message (always shown, like slog.Info replacement).
func Info(msg string, kvs ...any) {
	ts := time.Now().Format("15:04:05")
	line := fmt.Sprintf("%s%s%s %s", dim, ts, reset, msg)
	if len(kvs) > 0 {
		line += "  " + dim + formatKVs(kvs...) + reset
	}
	write(line + "\n")
}

// ── Helpers ─────────────────────────────────────────────────

func formatKVs(kvs ...any) string {
	var parts []string
	for i := 0; i+1 < len(kvs); i += 2 {
		k := fmt.Sprint(kvs[i])
		v := fmt.Sprint(kvs[i+1])
		if len(v) > 80 {
			v = v[:77] + "..."
		}
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, "  ")
}

func argsToKVs(args []any) []any {
	return args
}

func prettyJSON(s string) string {
	var obj any
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return s
	}
	pretty, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return s
	}
	return string(pretty)
}

func isSensitiveHeader(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, "key") || strings.Contains(k, "authorization") || strings.Contains(k, "token")
}
