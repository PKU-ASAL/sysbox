package driver

import (
	"fmt"
	"sync"
)

type Registry struct {
	mu      sync.RWMutex
	drivers map[string]Descriptor
}

func NewRegistry() *Registry { return &Registry{drivers: map[string]Descriptor{}} }
func (r *Registry) Register(descriptor Descriptor) error {
	if descriptor.Name == "" || descriptor.Version == "" {
		return fmt.Errorf("driver name and version are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.drivers[descriptor.Name]; exists {
		return fmt.Errorf("driver %q already registered", descriptor.Name)
	}
	r.drivers[descriptor.Name] = descriptor
	return nil
}
func (r *Registry) Get(name string) (Descriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	descriptor, ok := r.drivers[name]
	return descriptor, ok
}
func (r *Registry) Require(name string, capability Capability) (Descriptor, error) {
	descriptor, ok := r.Get(name)
	if !ok {
		return Descriptor{}, Wrap(ErrorNotFound, name, "driver is not registered", nil)
	}
	if descriptor.capability(capability) == nil {
		return Descriptor{}, Wrap(ErrorUnsupported, name, fmt.Sprintf("capability %s is not supported", capability), nil)
	}
	return descriptor, nil
}

func (r *Registry) RequireNode(name string) (Node, error) {
	descriptor, err := r.Require(name, CapabilityNode)
	if err != nil {
		return nil, err
	}
	return descriptor.Node, nil
}

func (r *Registry) RequireNIC(name string) (NIC, error) {
	d, err := r.Require(name, CapabilityNIC)
	if err != nil {
		return nil, err
	}
	return d.NIC, nil
}

func (r *Registry) RequireSnapshot(name string) (Snapshot, error) {
	d, err := r.Require(name, CapabilitySnapshot)
	if err != nil {
		return nil, err
	}
	return d.Snapshot, nil
}

func (r *Registry) RequireConsole(name string) (Console, error) {
	d, err := r.Require(name, CapabilityConsole)
	if err != nil {
		return nil, err
	}
	return d.Console, nil
}

func (r *Registry) RequireGuestExec(name string) (GuestExec, error) {
	d, err := r.Require(name, CapabilityGuestExec)
	if err != nil {
		return nil, err
	}
	return d.GuestExec, nil
}

func (r *Registry) RequireNetwork(name string) (Network, error) {
	d, err := r.Require(name, CapabilityNetwork)
	if err != nil {
		return nil, err
	}
	return d.Network, nil
}

func (r *Registry) RequireArtifact(name string) (Artifact, error) {
	d, err := r.Require(name, CapabilityArtifact)
	if err != nil {
		return nil, err
	}
	return d.Artifact, nil
}

func (r *Registry) RequireImport(name string) (Import, error) {
	d, err := r.Require(name, CapabilityImport)
	if err != nil {
		return nil, err
	}
	return d.Import, nil
}

func (r *Registry) RequireNodeState(name string) (NodeState, error) {
	d, err := r.Require(name, CapabilityNodeState)
	if err != nil {
		return nil, err
	}
	return d.NodeState, nil
}

func (r *Registry) RequirePower(name string) (Power, error) {
	d, err := r.Require(name, CapabilityPower)
	if err != nil {
		return nil, err
	}
	return d.Power, nil
}

func (r *Registry) RequireLinuxNetwork(name string) (LinuxNetwork, error) {
	d, err := r.Require(name, CapabilityLinuxNetwork)
	if err != nil {
		return nil, err
	}
	return d.LinuxNetwork, nil
}

func (r *Registry) RequireGuestNetwork(name string) (GuestNetwork, error) {
	d, err := r.Require(name, CapabilityGuestNetwork)
	if err != nil {
		return nil, err
	}
	return d.GuestNetwork, nil
}

func (r *Registry) RequirePolicy(name string) (Policy, error) {
	d, err := r.Require(name, CapabilityPolicy)
	if err != nil {
		return nil, err
	}
	return d.Policy, nil
}

var DefaultRegistry = NewRegistry()
