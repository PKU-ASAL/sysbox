package libvirt

import (
	"context"
	"strings"
	"testing"
)

func TestQGARejectsOversizedCallBeforeInvokingVirsh(t *testing.T) {
	c := &qgaConnection{domain: "no-domain"}
	err := c.call(context.Background(), "guest-exec", map[string]any{"input-data": strings.Repeat("a", 128<<10)}, nil)
	if err == nil || !strings.Contains(err.Error(), "QGA request exceeds") {
		t.Fatalf("want explicit size rejection, got %v", err)
	}
}
