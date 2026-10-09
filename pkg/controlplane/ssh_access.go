package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"golang.org/x/crypto/ssh"
	"net"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// SSHAccessRequest is the managed configuration contract. It contains public
// authorization material only; private keys, shell commands and host paths are
// deliberately outside this API.
type SSHAccessRequest struct {
	ID             string   `json:"id"`
	Topology       string   `json:"topology"`
	Node           string   `json:"node"`
	AuthorizedKeys []string `json:"authorized_keys"`
	BindIP         string   `json:"bind_ip,omitempty"`
	Port           int      `json:"port,omitempty"`
}

var sshAccessID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func (r SSHAccessRequest) Validate() error {
	if !sshAccessID.MatchString(r.ID) || !sshAccessID.MatchString(r.Topology) || !sshAccessID.MatchString(r.Node) || len(r.AuthorizedKeys) == 0 || len(r.AuthorizedKeys) > 64 {
		return fmt.Errorf("invalid managed ssh access request")
	}
	if r.BindIP != "" && net.ParseIP(r.BindIP) == nil {
		return fmt.Errorf("invalid ssh bind address")
	}
	if r.Port < 0 || r.Port > 65535 {
		return fmt.Errorf("invalid ssh port")
	}
	for _, k := range r.AuthorizedKeys {
		if err := validatePublicSSHKey(k); err != nil {
			return err
		}
	}
	return nil
}
func validatePublicSSHKey(key string) error {
	fields := strings.Fields(key)
	if !utf8.ValidString(key) || len(key) > 16384 || len(fields) < 2 {
		return fmt.Errorf("invalid ssh public key")
	}
	if strings.ContainsAny(key, "\x00\r\n") || strings.HasPrefix(fields[0], "ssh-private") {
		return fmt.Errorf("invalid ssh public key")
	}
	parsed, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil || len(options) != 0 || len(strings.TrimSpace(string(rest))) != 0 || parsed.Type() != fields[0] {
		return fmt.Errorf("invalid SSH public key or unsupported authorized-key options")
	}
	return nil
}

// AuthorizedKeysContent uses the same ordering as Digest. A reordered request
// cannot carry identical authority but install different file bytes.
func (r SSHAccessRequest) AuthorizedKeysContent() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	keys := append([]string(nil), r.AuthorizedKeys...)
	sort.Strings(keys)
	return []byte(strings.Join(keys, "\n") + "\n"), nil
}
func (r SSHAccessRequest) Digest() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	x := r
	x.AuthorizedKeys = append([]string(nil), r.AuthorizedKeys...)
	sort.Strings(x.AuthorizedKeys)
	raw, err := json.Marshal(x)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
