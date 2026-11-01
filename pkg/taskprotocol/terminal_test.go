package taskprotocol

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTerminalOptionsBindDigestAndRejectInvalidDimensions(t *testing.T) {
	base := Request{UserID: 65534, ID: "console", Args: []string{"/bin/sh"}, OutputLimit: 1024}
	plain, err := json.Marshal(base)
	require.NoError(t, err)
	digest, err := base.Digest()
	require.NoError(t, err)
	for _, test := range []struct {
		terminal string
		valid    bool
	}{
		{`{"tty":true,"cols":120,"rows":32}`, true},
		{`{"tty":false}`, true},
		{`{"tty":true,"cols":0,"rows":32}`, false},
		{`{"tty":true,"cols":65536,"rows":32}`, false},
		{`{"tty":true,"cols":80,"rows":-1}`, false},
	} {
		t.Run(test.terminal, func(t *testing.T) {
			var payload map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(plain, &payload))
			payload["terminal"] = json.RawMessage(test.terminal)
			raw, err := json.Marshal(payload)
			require.NoError(t, err)
			var terminal Request
			require.NoError(t, json.Unmarshal(raw, &terminal))
			other, err := terminal.Digest()
			require.NoError(t, err)
			require.NotEqual(t, digest, other, "terminal mode is part of the signed payload")
			if test.valid {
				require.NoError(t, terminal.Validate())
			} else {
				require.Error(t, terminal.Validate())
			}
		})
	}
}
