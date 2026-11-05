package hostsupervisor

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
)

// ValidateSigningKey rejects a malformed expanded key even if its stored public
// suffix matches a separate public file. Both installation and runtime use this
// check; no second implementation of Ed25519 belongs in deployment scripts.
func ValidateSigningKey(key []byte) error {
	if len(key) != ed25519.PrivateKeySize || !bytes.Equal(ed25519.NewKeyFromSeed(key[:ed25519.SeedSize]), key) {
		return fmt.Errorf("invalid host signing key")
	}
	return nil
}
