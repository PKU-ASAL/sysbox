package substrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

// SSHAccessConfig contributes keys to an existing SSH service. It does not
// change the service's listeners, users, or administrator-owned key files.
// An empty BindIP or zero Port accepts any local listener for this user.
type SSHAccessConfig struct {
	User           string   `json:"user"`
	AccessID       string   `json:"access_id"`
	AuthorizedKeys []string `json:"authorized_keys,omitempty"`
	BindIP         string   `json:"bind_ip,omitempty"`
	Port           int      `json:"port,omitempty"`
}

var sshAccessUser = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)

func ValidateSSHAccessUser(user string) error {
	if !sshAccessUser.MatchString(user) {
		return fmt.Errorf("invalid managed SSH user")
	}
	return nil
}

func ValidateSSHPublicKey(key string) error {
	fields := strings.Fields(key)
	if !utf8.ValidString(key) || len(key) > 16384 || len(fields) < 2 || strings.ContainsAny(key, "\x00\r\n") {
		return fmt.Errorf("invalid SSH public key")
	}
	parsed, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil || len(options) != 0 || len(strings.TrimSpace(string(rest))) != 0 || parsed.Type() != fields[0] {
		return fmt.Errorf("invalid SSH public key or unsupported authorized-key options")
	}
	return nil
}

type SSHAccessMutation struct {
	ID               string          `json:"id"`
	ExpectedRevision uint64          `json:"expected_revision"`
	Delete           bool            `json:"delete"`
	Config           SSHAccessConfig `json:"config"`
}

func (r SSHAccessMutation) Validate() error {
	if !fileInstallID.MatchString(r.ID) || !fileInstallID.MatchString(r.Config.AccessID) || ValidateSSHAccessUser(r.Config.User) != nil {
		return fmt.Errorf("invalid managed SSH identity")
	}
	if r.Delete {
		if r.ExpectedRevision == 0 || len(r.Config.AuthorizedKeys) != 0 || r.Config.BindIP != "" || r.Config.Port != 0 {
			return fmt.Errorf("SSH deletion requires an exact existing revision and no replacement configuration")
		}
		return nil
	}
	if len(r.Config.AuthorizedKeys) == 0 || len(r.Config.AuthorizedKeys) > 64 || r.Config.Port < 0 || r.Config.Port > 65535 || (r.Config.BindIP != "" && net.ParseIP(r.Config.BindIP) == nil) {
		return fmt.Errorf("invalid managed SSH configuration")
	}
	for _, key := range r.Config.AuthorizedKeys {
		if err := ValidateSSHPublicKey(key); err != nil {
			return err
		}
	}
	return nil
}

func (r SSHAccessMutation) Digest() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	r.Config.AuthorizedKeys = append([]string(nil), r.Config.AuthorizedKeys...)
	sort.Strings(r.Config.AuthorizedKeys)
	raw, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

type SSHAccessHandle struct {
	NodeID      string `json:"node_id,omitempty"`
	ID          string `json:"id"`
	Digest      string `json:"digest"`
	User        string `json:"user"`
	AccessID    string `json:"access_id"`
	Revision    uint64 `json:"revision"`
	BootID      string `json:"boot_id"`
	TopologyUID string `json:"topology_uid"`
	ResourceUID string `json:"resource_uid"`
	Generation  int64  `json:"generation"`
	Epoch       uint64 `json:"epoch"`
}

// SSHAccessResult is historical evidence of a committed configuration change.
// Current presence must be read using InspectSSHAccess.
type SSHAccessResult struct {
	Handle  SSHAccessHandle `json:"handle"`
	Present bool            `json:"present"`
}

type SSHAccessInspection struct {
	Status   string `json:"status"` // present, drifted, or absent
	Revision uint64 `json:"revision"`
}

type ConnectionSSHAccess interface {
	DiscoverSSHAccess(context.Context, SSHAccessQuery) (SSHAccessDiscovery, error)
	// MutateSSHAccess preserves the expected handle after dispatch errors for
	// observation. On error, Present is unknown, not evidence of absence.
	MutateSSHAccess(context.Context, SSHAccessMutation) (SSHAccessResult, error)
	ObserveSSHAccess(context.Context, SSHAccessHandle) (SSHAccessResult, error)
	InspectSSHAccess(context.Context, SSHAccessHandle) (SSHAccessInspection, error)
}
