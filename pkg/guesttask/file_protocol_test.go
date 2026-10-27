package guesttask

import (
	"bytes"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestFileProtocolAcceptsFull16MiBWithoutTaskStdinLimit(t *testing.T) {
	call := Call{Version: Version, Operation: "file-install", FileData: bytes.Repeat([]byte{'x'}, int(substrate.MaxFileInstallSize))}
	raw, err := json.Marshal(call)
	require.NoError(t, err)
	decoded, err := ReadCall(bytes.NewReader(raw))
	require.NoError(t, err)
	require.Len(t, decoded.FileData, int(substrate.MaxFileInstallSize))
	_, err = ReadCall(bytes.NewReader(bytes.Repeat([]byte{'x'}, MaxCallSize+1)))
	require.Error(t, err)
}
