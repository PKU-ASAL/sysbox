package commands

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostKeyCheckUsesStrictRuntimeKeyValidation(t *testing.T) {
	public, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	for _, kind := range []string{"valid", "seed-corrupt", "public-mismatch", "truncated", "trailing"} {
		t.Run(kind, func(t *testing.T) {
			payload := append(append([]byte(nil), key...), public...)
			switch kind {
			case "seed-corrupt":
				payload[0] ^= 1
			case "public-mismatch":
				payload[len(payload)-1] ^= 1
			case "truncated":
				payload = payload[:63]
			case "trailing":
				payload = append(payload, 0)
			}
			cmd := newHostKeyCheckCmd()
			cmd.SetIn(bytes.NewReader(payload))
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			err := cmd.Execute()
			if kind == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.NotContains(t, output.String(), string(key))
		})
	}
}

func TestHostKeyCheckDoesNotInitializeProviders(t *testing.T) {
	public, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	rootCmd.SetArgs([]string{"host-key-check"})
	rootCmd.SetIn(bytes.NewReader(append(key, public...)))
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	t.Cleanup(func() { rootCmd.SetArgs(nil); rootCmd.SetIn(nil); rootCmd.SetOut(nil); rootCmd.SetErr(nil) })
	require.NoError(t, ExecuteContextWithProviders(context.Background(), func(string) error { t.Error("key validation initialized providers"); return nil }))
}
