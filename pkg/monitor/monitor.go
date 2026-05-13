// Package monitor defines the pluggable EDR/sensor interface for sysbox.
//
// Design goals:
//   - Substrate-agnostic: Docker containers today, VMs/microVMs/Windows later.
//   - Backend-pluggable: tracee, sysdig, custom EDR, ETW — all via the same interface.
//   - Shallow normalisation: Event.Raw carries the full platform payload; the
//     normalised fields (NodeID, Category, PID, PPID) are just routing metadata.
//
// Usage:
//
//	b, _ := monitor.Get("tracee")
//	ch, _ := b.Start(ctx, targets, cfg)
//	for ev := range ch { ... }
package monitor

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/oslab/sysbox/pkg/sensor"
)

// Backend is the pluggable EDR/sensor interface.
// Implement this to connect tracee, sysdig, a custom EDR agent, ETW, etc.
//
// Lifecycle:
//
//	Start → event channel open → (events flow) → Stop or ctx cancelled → channel closed
type Backend interface {
	Name() string

	// Start activates monitoring for the given targets and returns a channel
	// of normalised events. The channel is closed when ctx is cancelled or
	// Stop is called.
	Start(ctx context.Context, targets []Target, cfg Config) (<-chan sensor.Event, error)

	// Stop tears down all deployed agents and flushes any buffered state.
	Stop(ctx context.Context) error
}

// Target describes a monitored node in substrate-neutral terms.
// The Handle map carries substrate-specific handles so the Backend
// can deploy its agent without knowing the substrate type directly.
type Target struct {
	NodeID    string            `json:"node_id"`   // logical name → Event.NodeID
	Substrate string            `json:"substrate"` // "docker" | "vm" | "microvm" | "windows"
	Handle    map[string]string `json:"handle"`    // substrate-specific:
	//   docker:  {"container_id": "...", "container_name": "sysbox-node_attack"}
	//   vm:      {"ssh_addr": "...", "ssh_key": "..."}
	//   microvm: {"vsock_cid": "..."}
}

// Config is the normalised monitor configuration derived from the HCL
// sysbox_monitor resource.
type Config struct {
	// Events is the list of system events to capture.
	// Semantics are backend-specific (tracee event names, sysdig filters, etc.).
	// Nil or empty means "use backend defaults".
	Events []string `json:"events"`

	// Extra holds backend-specific key-value configuration that does not
	// have a normalised counterpart (e.g. "sensor_container", "tracee_bin").
	Extra map[string]string `json:"extra"`
}

// ── Registry ──────────────────────────────────────────────────────────────────

var (
	mu       sync.RWMutex
	backends = map[string]Backend{}
)

// Register adds b to the global backend registry.
// Typically called from init() inside each backend sub-package.
func Register(b Backend) {
	mu.Lock()
	defer mu.Unlock()
	backends[b.Name()] = b
}

// Get returns the named backend or an error if not registered.
func Get(name string) (Backend, error) {
	mu.RLock()
	defer mu.RUnlock()
	b, ok := backends[name]
	if !ok {
		return nil, fmt.Errorf("monitor backend %q not registered (available: %s)", name, List())
	}
	return b, nil
}

// List returns a sorted slice of registered backend names.
func List() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(backends))
	for n := range backends {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
