package substrate

import (
	"fmt"
	"sync"
)

var (
	registry   = make(map[string]Substrate)
	registryMu sync.Mutex
)

// Register adds a substrate under its name.
func Register(s Substrate) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[s.Name()] = s
}

// Get returns a registered substrate by name. Returns an error if not found.
func Get(name string) (Substrate, error) {
	registryMu.Lock()
	defer registryMu.Unlock()
	s, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("substrate %q not registered", name)
	}
	return s, nil
}

// List returns all registered substrate names (for diagnostics).
func List() []string {
	registryMu.Lock()
	defer registryMu.Unlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	return names
}
