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
	"github.com/oslab/sysbox/pkg/value"
)

// SchemaVersion is the current persistent format version.
//
//	v1 – sysbox v0.x (pre-multi-substrate cleanup)
//	v2 – sysbox v1.0 (typed NodeHandle, ProviderExtra blob)
const SchemaVersion = 4

type ResourceStatus string

const (
	ResourcePresent  ResourceStatus = "present"
	ResourceAbsent   ResourceStatus = "absent"
	ResourceDrifted  ResourceStatus = "drifted"
	ResourceDegraded ResourceStatus = "degraded"
	ResourceUnknown  ResourceStatus = "unknown"
)

type State struct {
	mu        sync.RWMutex `json:"-"`
	Version   int          `json:"version"`
	Lineage   string       `json:"lineage"`
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
	Address       address.Address   `json:"address"`
	ResourceType  string            `json:"resource_type"`
	Driver        string            `json:"driver"`
	SchemaVersion int               `json:"schema_version"`
	ExternalID    string            `json:"external_id,omitempty"`
	Attributes    Attributes        `json:"attributes"`
	Private       json.RawMessage   `json:"private,omitempty"`
	Dependencies  []address.Address `json:"dependencies,omitempty"`
	Status        ResourceStatus    `json:"status"`
	CreatedAt     time.Time         `json:"created_at,omitempty"`
	UpdatedAt     time.Time         `json:"updated_at,omitempty"`
}

// Int returns the value at key as an int. JSON round-trip stores numbers as
// float64, so both int and float64 are accepted. Returns 0 if the key is
// missing or the type doesn't match.
func (r *Resource) Int(key string) int {
	switch v := r.lookup(key).(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

// Str returns the value at key as a string. Returns "" if missing or wrong type.
func (r *Resource) Str(key string) string {
	s, _ := r.lookup(key).(string)
	return s
}

// Slice returns the value at key as []any. Returns nil if missing or wrong type.
func (r *Resource) Slice(key string) []any {
	v, _ := r.lookup(key).([]any)
	return v
}

// Map returns the value at key as map[string]any. Returns nil if missing.
func (r *Resource) Map(key string) map[string]any {
	v, _ := r.lookup(key).(map[string]any)
	return v
}

// Float returns the value at key as float64. Returns 0 if missing or wrong type.
func (r *Resource) Float(key string) float64 {
	switch v := r.lookup(key).(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return 0
}

// Bool returns the value at key as bool. Returns false if missing or wrong type.
func (r *Resource) Bool(key string) bool {
	b, _ := r.lookup(key).(bool)
	return b
}

type Attributes map[string]any

func NewAttributes(input map[string]any) (Attributes, error) {
	typed, err := value.FromGo(input)
	if err != nil {
		return nil, err
	}
	output, _ := typed.GoValue().(map[string]any)
	return Attributes(output), nil
}
func (a Attributes) GoValue() any {
	typed, err := value.FromGo(map[string]any(a))
	if err != nil {
		return nil
	}
	return typed.GoValue()
}
func (a Attributes) TypedValue() (value.Value, error) { return value.FromGo(map[string]any(a)) }
func (r *Resource) attributeMap() map[string]any {
	if r == nil {
		return nil
	}
	return map[string]any(r.Attributes)
}
func (r *Resource) AttributeMap() map[string]any { return r.attributeMap() }
func (r *Resource) lookup(key string) any {
	if value, ok := r.attributeMap()[key]; ok {
		return value
	}
	return r.RuntimeValue(key)
}

var runtimePrivateKeys = map[string]bool{"container_id": true, "pid": true, "netns": true, "bridge": true, "docker_network_id": true, "image_id": true, "vm_dir": true, "tap_name": true}

func MustAttributes(input map[string]any) Attributes {
	result, err := NewAttributes(input)
	if err != nil {
		panic(err)
	}
	return result
}
func (r *Resource) SetAttribute(key string, item any) error {
	if runtimePrivateKeys[key] {
		delete(r.Attributes, key)
		return r.SetRuntimeValue(key, item)
	}
	values := r.attributeMap()
	if values == nil {
		values = map[string]any{}
	}
	values[key] = item
	converted, err := NewAttributes(values)
	if err != nil {
		return err
	}
	r.Attributes = converted
	return nil
}

// Convenience accessors for well-known instance keys. These centralise
// key names and eliminate scattered raw type assertions.

func (r *Resource) ContainerID() string           { return r.Str("container_id") }
func (r *Resource) PrimaryIP() string             { return r.Str("primary_ip") }
func (r *Resource) IsNAT() bool                   { return r.Bool("nat") }
func (r *Resource) DockerNetID() string           { return r.Str("docker_network_id") }
func (r *Resource) PID() int                      { return r.Int("pid") }
func (r *Resource) LifecyclePreventDestroy() bool { return r.Bool("lifecycle_prevent_destroy") }
func (r *Resource) ImageID() string               { return r.Str("image_id") }
func (r *Resource) Repository() string            { return r.Str("repository") }
func (r *Resource) NetNS() string                 { return r.Str("netns") }
func (r *Resource) Bridge() string                { return r.Str("bridge") }

// ReconstructHandle combines public observations with opaque driver state.
func (r *Resource) ReconstructHandle(sub substrate.Substrate) (substrate.NodeHandle, error) {
	handle := substrate.NodeHandle{
		ID:  r.ContainerID(),
		Net: substrate.NetInfo{PrimaryIP: r.PrimaryIP()},
	}
	if blob, err := r.ProviderState(); err != nil {
		return handle, err
	} else if len(blob) > 0 {
		ps, err := sub.UnmarshalProviderState(blob)
		if err != nil {
			return handle, fmt.Errorf("resource %s: corrupt provider state: %w", r.Address, err)
		}
		handle.Provider = ps
	}
	return handle, nil
}

func (s *State) Marshal() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Resources {
		normalizeResource(&s.Resources[i])
	}
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
	return &s, nil
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
	normalizeResource(&r)
	if r.ResourceType == "" {
		r.ResourceType = r.Address.Type
	}
	if r.SchemaVersion == 0 {
		r.SchemaVersion = 1
	}
	if r.Status == "" {
		r.Status = ResourcePresent
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	r.UpdatedAt = r.CreatedAt
	s.Resources = append(s.Resources, r)
}

func normalizeResource(r *Resource) {
	for key := range runtimePrivateKeys {
		if item, exists := r.Attributes[key]; exists {
			delete(r.Attributes, key)
			_ = r.SetRuntimeValue(key, item)
		}
	}
	if r.ExternalID == "" {
		r.ExternalID = r.Str("container_id")
		if r.ExternalID == "" {
			r.ExternalID = r.Str("docker_network_id")
		}
	}
	if r.ResourceType == "" {
		r.ResourceType = r.Address.Type
	}
	if r.SchemaVersion == 0 {
		r.SchemaVersion = 1
	}
	if r.Status == "" {
		r.Status = ResourcePresent
	}
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
