package agentexec

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGuestOutputBudgetKeepsDefaultAndExplicitBound(t *testing.T) {
	input := strings.Repeat("x", (5<<20)+1)
	for _, tc := range []struct {
		limit, want int
		truncated   bool
	}{
		{0, guestOutputLimit, true}, {5 << 20, 5 << 20, true}, {1024, 1024, true},
	} {
		result := encodeGuestOutputWithLimit(input, "", tc.limit)
		raw, err := base64.StdEncoding.DecodeString(result.Stdout)
		if err != nil || len(raw) != tc.want || result.Truncated != tc.truncated {
			t.Fatalf("limit %d: bytes=%d truncated=%v err=%v", tc.limit, len(raw), result.Truncated, err)
		}
	}
}
