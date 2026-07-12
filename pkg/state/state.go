// Package state manages sysbox's persistent state file.
//
// Each sysbox apply writes resource entries to a JSON state file.
// The state is the single source of truth for what's currently deployed.
//
// SchemaVersion is bumped on every breaking format change. v1.0 introduces
// schema v2: typed NodeHandle, ProviderExtra json blob, no v1→v2 migration.
// Loading a v1 file fails with a clear error pointing users to clear .sysbox/runs/.
package state

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/substrate"
)

// SchemaVersion is the current persistent format version.
//
//	v1 – sysbox v0.x (pre-multi-substrate cleanup)
//	v2 – sysbox v1.0 (typed NodeHandle, ProviderExtra blob)
const SchemaVersion = 3

type State struct {
	mu        sync.RWMutex `json:"-"`
	Version   int          `json:"version"`
	RunID     string       `json:"run_id"`
	Resources []Resource   `json:"resources"`
	Meta      StateMeta    `json:"-"`
}

type StateMeta struct {
	Backend   string
	Serial    int64
	Exists    bool
	UpdatedAt time.Time
}

type Resource struct {
	Address   address.Address `json:"address"`
	Provider  string          `json:"provider"`
	Instance  map[string]any  `json:"instance"`
	CreatedAt string          `json:"created_at,omitempty"`
	UpdatedAt string          `json:"updated_at,omitempty"`
}

// Int returns the value at key as an int. JSON round-trip stores numbers as
// float64, so both int and float64 are accepted. Returns 0 if the key is
// missing or the type doesn't match.
func (r *Resource) Int(key string) int {
	switch v := r.Instance[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

// Str returns the value at key as a string. Returns "" if missing or wrong type.
func (r *Resource) Str(key string) string {
	s, _ := r.Instance[key].(string)
	return s
}

// Slice returns the value at key as []any. Returns nil if missing or wrong type.
func (r *Resource) Slice(key string) []any {
	v, _ := r.Instance[key].([]any)
	return v
}

// Map returns the value at key as map[string]any. Returns nil if missing.
func (r *Resource) Map(key string) map[string]any {
	v, _ := r.Instance[key].(map[string]any)
	return v
}

// Float returns the value at key as float64. Returns 0 if missing or wrong type.
func (r *Resource) Float(key string) float64 {
	switch v := r.Instance[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return 0
}

// Bool returns the value at key as bool. Returns false if missing or wrong type.
func (r *Resource) Bool(key string) bool {
	b, _ := r.Instance[key].(bool)
	return b
}

// Convenience accessors for well-known instance keys. These centralise
// key names and eliminate scattered raw type assertions.

func (r *Resource) ContainerID() string           { return r.Str("container_id") }
func (r *Resource) PrimaryIP() string             { return r.Str("primary_ip") }
func (r *Resource) ProviderExtra() string         { return r.Str("provider_extra") }
func (r *Resource) IsNAT() bool                   { return r.Bool("nat") }
func (r *Resource) DockerNetID() string           { return r.Str("docker_network_id") }
func (r *Resource) PID() int                      { return r.Int("pid") }
func (r *Resource) LifecyclePreventDestroy() bool { return r.Bool("lifecycle_prevent_destroy") }
func (r *Resource) ImageID() string               { return r.Str("image_id") }
func (r *Resource) Repository() string            { return r.Str("repository") }
func (r *Resource) NetNS() string                 { return r.Str("netns") }
func (r *Resource) Bridge() string                { return r.Str("bridge") }

// ReconstructHandle rebuilds a substrate.NodeHandle from the resource's
// persisted instance data. This replaces the hand-assembled pattern of
// reading container_id + primary_ip + provider_extra that was scattered
// across the API, commands, and runtime packages.
func (r *Resource) ReconstructHandle(sub substrate.Substrate) (substrate.NodeHandle, error) {
	handle := substrate.NodeHandle{
		ID:  r.ContainerID(),
		Net: substrate.NetInfo{PrimaryIP: r.PrimaryIP()},
	}
	if blob := r.ProviderExtra(); blob != "" {
		ps, err := sub.UnmarshalProviderState([]byte(blob))
		if err != nil {
			return handle, fmt.Errorf("resource %s: corrupt provider state: %w", r.Address, err)
		}
		handle.Provider = ps
	}
	return handle, nil
}

func (s *State) Marshal() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return json.MarshalIndent(s, "", "  ")
}

func Unmarshal(data []byte) (*State, error) {
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}
	if s.Version != SchemaVersion {
		return nil, &IncompatibleVersionError{Found: s.Version, Expected: SchemaVersion}
	}
	// Migrate: older state files for sysbox_router lacked primary_ip.
	// Backfill from the first NIC that has an IP address.
	for i := range s.Resources {
		migratePrimaryIP(&s.Resources[i])
	}
	return &s, nil
}

// migratePrimaryIP fills in a missing primary_ip from NIC data.
// Pre-v1.0 router entries didn't write primary_ip; the NIC list has it.
func migratePrimaryIP(r *Resource) {
	if r.Str("primary_ip") != "" {
		return
	}
	nics, _ := r.Instance["nics"].([]any)
	for _, n := range nics {
		m, _ := n.(map[string]any)
		if ip, _ := m["ip"].(string); ip != "" {
			// Strip CIDR suffix.
			for j := 0; j < len(ip); j++ {
				if ip[j] == '/' {
					ip = ip[:j]
					break
				}
			}
			r.Instance["primary_ip"] = ip
			return
		}
	}
}

// IncompatibleVersionError is returned when state belongs to another breaking
// schema generation. Sysbox never mutates incompatible state implicitly.
type IncompatibleVersionError struct {
	Found    int
	Expected int
}

func (e *IncompatibleVersionError) Error() string {
	return fmt.Sprintf(
		"state schema v%d is incompatible with sysbox binary (expects v%d). "+
			"destroy the lab with the binary that created this state before upgrading",
		e.Found, e.Expected,
	)
}

func (s *State) FindResource(addr address.Address) *Resource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.Resources {
		if s.Resources[i].Address.Equal(addr) {
			return &s.Resources[i]
		}
	}
	return nil
}

func (s *State) AddResource(r Resource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.Address = r.Address.Clone()
	if r.CreatedAt == "" {
		r.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	r.UpdatedAt = r.CreatedAt
	s.Resources = append(s.Resources, r)
}

func (s *State) RemoveResource(addr address.Address) {
	s.mu.Lock()
	defer s.mu.Unlock()
	filtered := make([]Resource, 0, len(s.Resources))
	for _, r := range s.Resources {
		if r.Address.Equal(addr) {
			continue
		}
		filtered = append(filtered, r)
	}
	s.Resources = filtered
}
