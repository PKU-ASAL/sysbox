package commands

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"io"

	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/spf13/cobra"
)

// Installation checks only the bytes supplied on stdin: no root requirement,
// configuration initialization, provider access, or secret-bearing arguments.
func newHostKeyCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use: "host-key-check", Short: "Validate a raw host private/public key pair from stdin",
		Args: cobra.NoArgs, Annotations: map[string]string{"service-client": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			const size = ed25519.PrivateKeySize + ed25519.PublicKeySize
			raw, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), size+1))
			if err != nil {
				return fmt.Errorf("read host signing key input: %w", err)
			}
			if len(raw) != size {
				return fmt.Errorf("host signing key check requires 64 private and 32 public bytes")
			}
			key, public := raw[:ed25519.PrivateKeySize], raw[ed25519.PrivateKeySize:]
			if err := hostsupervisor.ValidateSigningKey(key); err != nil {
				return err
			}
			if !bytes.Equal(ed25519.PrivateKey(key).Public().(ed25519.PublicKey), public) {
				return fmt.Errorf("host signing key public mismatch")
			}
			return nil
		},
	}
}
