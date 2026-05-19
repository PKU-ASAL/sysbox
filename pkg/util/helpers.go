package util

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
)

// AsString extracts a string value from an any. Returns empty string
// for nil or non-string values.
func AsString(v any) string {
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// AsInt extracts an int value from an any. JSON numbers decode as float64,
// so this handles both int and float64. Returns 0 for nil or non-numeric.
func AsInt(v any) int {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	}
	return 0
}

// AsBool extracts a bool value from an any. Returns false for nil or non-bool.
func AsBool(v any) bool {
	if v == nil {
		return false
	}
	b, _ := v.(bool)
	return b
}

// ShellQuote wraps a string in single quotes with embedded single-quote
// escaping. Safe for passing through a remote shell — unlike Go's %q
// (double-quoted), single-quoted strings are not subject to $, `, or !
// expansion by the receiving shell.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ShellQuoteJoin joins multiple arguments into a sh-safe single string,
// quoting each argument with ShellQuote.
func ShellQuoteJoin(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(ShellQuote(a))
	}
	return b.String()
}

// EnvToSlice converts a map[string]string environment to a slice of
// "KEY=VALUE" strings suitable for exec.Cmd.Env.
func EnvToSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// BestEffort runs fn and logs any error at warn level. Intended for cleanup
// and teardown paths where a failure should not abort the caller but should
// still be observable. Returns the error so callers can optionally inspect it.
func BestEffort(fn func() error, msg string) error {
	if err := fn(); err != nil {
		slog.Warn(msg, "error", err)
		return err
	}
	return nil
}

// BestEffortClose closes an io.Closer and logs any error. A convenience
// wrapper around BestEffort for the common close-on-defer pattern.
func BestEffortClose(c io.Closer, name string) {
	BestEffort(c.Close, name+" close failed")
}

// BestEffortIgnore runs fn and logs any error at warn level, then discards
// it. Sugar for BestEffort when the caller never inspects the return value.
func BestEffortIgnore(fn func() error, msg string) {
	_ = BestEffort(fn, msg)
}
