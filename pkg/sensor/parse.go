package sensor

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ParseTraceeJSON parses a single line of tracee JSON output into an Event.
// NodeID is inferred from the container.name field when it carries the
// "sysbox-<node>" prefix; callers may override after the call.
// Returns an error if the line is not valid JSON.
func ParseTraceeJSON(line []byte, out *Event) error {
	var raw map[string]any
	if err := json.Unmarshal(line, &raw); err != nil {
		return err
	}
	*out = parseEvent("", line, raw)
	return nil
}

// parseEvent converts a raw tracee JSON map into an Event.
// line is stored verbatim in Event.Raw.
//
// Tracee v0.20+ field names:
//   timestamp, hostProcessId, hostParentProcessId, eventName,
//   container{name} (Docker enrichment)
func parseEvent(nodeID string, line []byte, raw map[string]any) Event {
	if nodeID == "" {
		if container, ok := raw["container"].(map[string]any); ok {
			if name, ok := container["name"].(string); ok && strings.HasPrefix(name, "sysbox-") {
				nodeID = strings.TrimPrefix(name, "sysbox-")
			}
		}
	}

	var category string
	switch strField(raw, "eventName") {
	case "execve", "execveat":
		category = "exec"
	case "openat", "open":
		category = "file"
	case "connect", "accept", "accept4", "bind":
		category = "net"
	default:
		category = "process"
	}

	raw_ := make([]byte, len(line))
	copy(raw_, line)

	return Event{
		NodeID:    nodeID,
		Timestamp: int64(floatField(raw, "timestamp")),
		Category:  category,
		PID:       int(floatField(raw, "hostProcessId")),
		PPID:      int(floatField(raw, "hostParentProcessId")),
		Raw:       json.RawMessage(raw_),
	}
}

func floatField(m map[string]any, key string) float64 {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	}
	return 0
}

func strField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}
