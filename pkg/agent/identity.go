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
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/controlplane"
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
	APIURL       string
	Token        string
	ID           string
	Name         string
	Capabilities []string
	Labels       map[string]string
	Path         string
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
	ident := &Identity{
		ID:           id,
		Name:         opts.Name,
		APIURL:       strings.TrimRight(opts.APIURL, "/"),
		Token:        opts.Token,
		Secret:       secret,
		Capabilities: normalizeCapabilities(opts.Capabilities),
		Labels:       opts.Labels,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if ident.Name == "" {
		ident.Name = ident.ID
	}
	if len(ident.Capabilities) == 0 {
		ident.Capabilities = DefaultCapabilities()
	}
	if ident.Labels == nil {
		ident.Labels = map[string]string{}
	}
	ident.Labels["mode"] = "agent"
	ident.Labels["os"] = runtime.GOOS
	ident.Labels["arch"] = runtime.GOARCH
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
	return postAgent(ctx, ident, ident.APIURL+"/v1/agents", ident.Worker())
}

func (i *Identity) Worker() controlplane.Worker {
	now := time.Now().UTC()
	return controlplane.Worker{
		ID:            i.ID,
		Name:          i.Name,
		Status:        "online",
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

func DefaultCapabilities() []string {
	return []string{"docker", "network", "firecracker", "kvm", "libvirt"}
}

func normalizeCapabilities(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func randomSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
