package agent

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestWorkerPrivilegesRequireAllCapabilitiesDropped(t *testing.T) {
	status := "CapEff:\t0000000000000000\nCapPrm:\t0000000000000000\nCapAmb:\t0000000000000000\nNoNewPrivs:\t1\n"
	require.NoError(t, validateWorkerStatus(status))
	require.Error(t, validateWorkerStatus(strings.Replace(status, "CapEff:\t0000000000000000", "CapEff:\t0000000000000001", 1)))
	require.Error(t, validateWorkerStatus(strings.Replace(status, "NoNewPrivs:\t1", "NoNewPrivs:\t0", 1)))
}
