package agentexec

import (
	"fmt"
	"strings"
)

func splitAddr(addr string) (string, string, error) {
	parts := strings.SplitN(addr, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected type.name, got %q", addr)
	}
	return parts[0], parts[1], nil
}
