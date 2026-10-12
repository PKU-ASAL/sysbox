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

	"github.com/oslab/sysbox/pkg/substrate"
)

// SSHAccessRequest is the managed configuration contract. It contains public
// authorization material only; private keys, shell commands and host paths are
// deliberately outside this API.
type SSHAccessRequest struct {
	// ID identifies this immutable operation; AccessID identifies the managed
	// configuration across distinct apply/rotate/delete operations.
	ID               string   `json:"id"`
	AccessID         string   `json:"access_id"`
	Topology         string   `json:"topology"`
	Node             string   `json:"node"`
	AuthorizedKeys   []string `json:"authorized_keys"`
	BindIP           string   `json:"bind_ip,omitempty"`
	Port             int      `json:"port,omitempty"`
	ExpectedRevision uint64   `json:"expected_revision,omitempty"`
}

var sshAccessID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func (r SSHAccessRequest) Validate() error {
	if !sshAccessID.MatchString(r.ID) || !sshAccessID.MatchString(r.AccessID) || !sshAccessID.MatchString(r.Topology) || !sshAccessID.MatchString(r.Node) || len(r.AuthorizedKeys) == 0 || len(r.AuthorizedKeys) > 64 {
		return fmt.Errorf("invalid managed ssh access request")
	}
	if r.BindIP != "" && net.ParseIP(r.BindIP) == nil {
		return fmt.Errorf("invalid ssh bind address")
	}
	if r.Port < 0 || r.Port > 65535 || r.ExpectedRevision == ^uint64(0) {
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
	return substrate.ValidateSSHPublicKey(key)
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

func (r SSHAccessRequest) Mutation(user string, delete bool) substrate.SSHAccessMutation {
	return substrate.SSHAccessMutation{ID: r.ID, ExpectedRevision: r.ExpectedRevision, Delete: delete,
		Config: substrate.SSHAccessConfig{User: user, AccessID: r.AccessID, AuthorizedKeys: append([]string(nil), r.AuthorizedKeys...), BindIP: r.BindIP, Port: r.Port}}
}
