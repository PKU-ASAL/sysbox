package runtime

import (
	"fmt"
	"io"
)

func WritePreflightLogs(w io.Writer, res *PreflightResult) {
	if res == nil {
		return
	}
	_, _ = w.Write([]byte("Preflight checks:\n"))
	for _, c := range res.Checks {
		status := "ok"
		if !c.OK {
			status = c.Severity
		}
		line := fmt.Sprintf("  [%s] %s", status, c.Name)
		if c.Message != "" {
			line += ": " + c.Message
		}
		if c.Hint != "" {
			line += " (" + c.Hint + ")"
		}
		line += "\n"
		_, _ = w.Write([]byte(line))
	}
}
