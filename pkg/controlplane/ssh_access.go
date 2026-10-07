package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
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
	keys := append([]string(nil), r.AuthorizedKeys...)
	for _, k := range keys {
		if err := validatePublicSSHKey(k); err != nil {
			return err
		}
	}
	return nil
}
func validatePublicSSHKey(key string) error {
	fields := strings.Fields(key)
	if len(fields) < 2 {
		return fmt.Errorf("invalid ssh public key")
	}
	if strings.ContainsAny(key, "\x00\r\n") || strings.HasPrefix(fields[0], "ssh-private") {
		return fmt.Errorf("invalid ssh public key")
	}
	return nil
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
