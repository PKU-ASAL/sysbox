package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

const DefaultIdentityPath = "/var/lib/sysbox/agent/identity.json"

type Identity struct {
	ID           string            `json:"id"`
	Name         string            `json:"name,omitempty"`
	APIURL       string            `json:"api_url"`
	Token        string            `json:"token,omitempty"`
	Secret       string            `json:"secret,omitempty"`
	Capabilities []string          `json:"capabilities,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

type RegisterOptions struct {
	APIURL     string
	Token      string
	ID         string
	Name       string
	Substrates []string
	Labels     map[string]string
	Path       string
}

func Register(ctx context.Context, opts RegisterOptions) (*Identity, error) {
	if opts.APIURL == "" {
		return nil, fmt.Errorf("api url is required")
	}
	if opts.Path == "" {
		opts.Path = DefaultIdentityPath
	}
	now := time.Now().UTC()
	id := opts.ID
	if id == "" {
		id = "agent-" + uuid.New().String()
	}
	secret, err := randomSecret()
	if err != nil {
		return nil, err
	}
	// Resolve capabilities from the declared substrates (explicit) or by
	// runtime detection. Detection results become visible labels so an operator
	// can see exactly which substrates were claimed and why.
	var caps []string
	var detection map[string]string
	if len(opts.Substrates) > 0 {
		caps = DeriveCapabilities(opts.Substrates)
		detection = detectExplicitReference(opts.Substrates)
	} else {
		caps, detection = DetectCapabilities()
	}

	ident := &Identity{
		ID:           id,
		Name:         opts.Name,
		APIURL:       strings.TrimRight(opts.APIURL, "/"),
		Token:        opts.Token,
		Secret:       secret,
		Capabilities: caps,
		Labels:       opts.Labels,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if ident.Name == "" {
		ident.Name = ident.ID
	}
	if ident.Labels == nil {
		ident.Labels = map[string]string{}
	}
	ident.Labels["mode"] = "agent"
	ident.Labels["os"] = runtime.GOOS
	ident.Labels["arch"] = runtime.GOARCH
	for name, status := range detection {
		ident.Labels["substrate."+name] = status
	}
	if err := RegisterRemote(ctx, ident); err != nil {
		return nil, err
	}
	if err := SaveIdentity(opts.Path, ident); err != nil {
		return nil, err
	}
	return ident, nil
}

func LoadIdentity(path string) (*Identity, error) {
	if path == "" {
		path = DefaultIdentityPath
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ident Identity
	if err := json.Unmarshal(raw, &ident); err != nil {
		return nil, fmt.Errorf("decode agent identity: %w", err)
	}
	if ident.ID == "" || ident.APIURL == "" {
		return nil, fmt.Errorf("agent identity is incomplete")
	}
	return &ident, nil
}

func SaveIdentity(path string, ident *Identity) error {
	if path == "" {
		path = DefaultIdentityPath
	}
	if ident == nil {
		return fmt.Errorf("identity is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(ident, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func RemoveIdentity(path string) error {
	if path == "" {
		path = DefaultIdentityPath
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func RegisterRemote(ctx context.Context, ident *Identity) error {
	return postAgent(ctx, ident, ident.APIURL+"/v1/agents", ident.Agent())
}

func (i *Identity) Agent() controlplane.Agent {
	now := time.Now().UTC()
	return controlplane.Agent{
		ID:            i.ID,
		Name:          i.Name,
		Status:        "online",
		AuthSecret:    i.Secret,
		SecretHash:    SecretHash(i.Secret),
		Protocol:      controlplane.AgentProtocolVersion,
		Capabilities:  i.Capabilities,
		Labels:        i.Labels,
		LastHeartbeat: now,
		UpdatedAt:     now,
		Version:       "dev",
	}
}

func postAgent(ctx context.Context, ident *Identity, url string, in any) error {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(in); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if ident.Token != "" {
		req.Header.Set("Authorization", "Bearer "+ident.Token)
	}
	if err := SignRequest(req, ident.ID, ident.Secret, time.Now()); err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s: %s", url, resp.Status)
	}
	return nil
}

// DetectCapabilities probes which substrates this host can actually run, and
// returns the capability list derived from them plus a per-substrate detection
// map ("ok" or "no-<check>") that becomes visible labels.
func DetectCapabilities() (caps []string, detection map[string]string) {
	return detectCapabilitiesFrom(driver.DefaultRegistry)
}

func detectCapabilitiesFrom(reg *driver.Registry) (caps []string, detection map[string]string) {
	names := reg.SubstrateNames()
	detection = detectFrom(reg, names)
	supported := make([]string, 0, len(names))
	for _, name := range names {
		if detection[name] == "ok" {
			supported = append(supported, name)
		}
	}
	return deriveCapabilitiesFrom(reg, supported), detection
}

// DeriveCapabilities derives the agent capability list from an explicit set of
// substrate names, plus the built-in "network" capability. NIC kinds (veth,
// tap) are pulled from each substrate's Capabilities, so callers declare
// substrates and never write capability names by hand.
func DeriveCapabilities(substrates []string) []string {
	return deriveCapabilitiesFrom(driver.DefaultRegistry, substrates)
}

func deriveCapabilitiesFrom(reg *driver.Registry, substrates []string) []string {
	set := map[string]bool{"network": true}
	for _, name := range substrates {
		node, err := reg.RequireNode(name)
		if err != nil {
			set[name] = true // unregistered substrate: claim the name only
			continue
		}
		for _, cap := range substrate.CapabilityNames(name, node.Capabilities()) {
			set[cap] = true
		}
	}
	out := make([]string, 0, len(set))
	for cap := range set {
		out = append(out, cap)
	}
	sort.Strings(out)
	return out
}

// detect runs PreflightChecks(true) for each named substrate and reports
// "ok" or "no-<check>" per substrate.
func detectFrom(reg *driver.Registry, names []string) map[string]string {
	checks := make(map[string][]substrate.PreflightCheck, len(names))
	for _, name := range names {
		if node, err := reg.RequireNode(name); err == nil {
			checks[name] = node.PreflightChecks(true)
		}
	}
	_, reasons := substrate.SupportedSubstrates(checks)
	detection := make(map[string]string, len(names))
	for _, name := range names {
		if reason, ok := reasons[name]; ok {
			detection[name] = "no-" + reason
		} else {
			detection[name] = "ok"
		}
	}
	return detection
}

// detectExplicitReference runs detection against explicitly declared substrates
// for reference only: it never vetoes the declaration, but marks a declared
// substrate that failed detection as "claimed-but-..." so the discrepancy is
// visible instead of silent.
func detectExplicitReference(explicit []string) map[string]string {
	return detectExplicitReferenceFrom(driver.DefaultRegistry, explicit)
}

func detectExplicitReferenceFrom(reg *driver.Registry, explicit []string) map[string]string {
	detection := detectFrom(reg, explicit)
	for name, status := range detection {
		if status != "ok" {
			detection[name] = "claimed-but-" + status
		}
	}
	return detection
}

func randomSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
