package sensor

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const execveJSON = `{"timestamp":1000000,"hostProcessId":1234,"hostParentProcessId":100,"cgroupId":999,"processName":"nmap","eventName":"execve","args":[{"name":"pathname","type":"const char*","value":"/usr/bin/nmap"}]}`
const cloneJSON = `{"timestamp":999000,"hostProcessId":100,"hostParentProcessId":50,"cgroupId":0,"processName":"bash","eventName":"clone","returnValue":1234,"args":[]}`
const connectJSON = `{"timestamp":2000000,"hostProcessId":5678,"hostParentProcessId":1,"eventName":"connect","args":[]}`
const containerJSON = `{"timestamp":1000000,"hostProcessId":42,"hostParentProcessId":1,"eventName":"execve","args":[],"container":{"name":"sysbox-node_attack","id":"abc123"}}`

func TestParseTraceeJSON_Exec(t *testing.T) {
	var ev Event
	require.NoError(t, ParseTraceeJSON([]byte(execveJSON), &ev))

	require.Equal(t, 1234, ev.PID)
	require.Equal(t, 100, ev.PPID)
	require.Equal(t, "exec", ev.Category)
	require.Equal(t, int64(1000000), ev.Timestamp)
	require.NotEmpty(t, ev.Raw)
}

func TestParseTraceeJSON_RawFieldsAccessible(t *testing.T) {
	var ev Event
	require.NoError(t, ParseTraceeJSON([]byte(execveJSON), &ev))

	fields := ev.RawFields()
	require.Equal(t, "execve", fields["eventName"])
	require.Equal(t, float64(999), fields["cgroupId"])
	args := fields["args"].([]any)
	arg0 := args[0].(map[string]any)
	require.Equal(t, "/usr/bin/nmap", arg0["value"])
}

func TestParseTraceeJSON_CategoryMapping(t *testing.T) {
	cases := []struct {
		json     string
		wantCat  string
	}{
		{execveJSON, "exec"},
		{cloneJSON, "process"},
		{connectJSON, "net"},
	}
	for _, tc := range cases {
		var ev Event
		require.NoError(t, ParseTraceeJSON([]byte(tc.json), &ev))
		require.Equal(t, tc.wantCat, ev.Category, "input: %s", tc.json)
	}
}

func TestParseTraceeJSON_ContainerNodeID(t *testing.T) {
	var ev Event
	require.NoError(t, ParseTraceeJSON([]byte(containerJSON), &ev))
	require.Equal(t, "node_attack", ev.NodeID)
}

func TestParseTraceeJSON_UnknownContainer(t *testing.T) {
	// Container name without sysbox- prefix → NodeID stays empty.
	const j = `{"hostProcessId":1,"eventName":"execve","args":[],"container":{"name":"unrelated-container"}}`
	var ev Event
	require.NoError(t, ParseTraceeJSON([]byte(j), &ev))
	require.Empty(t, ev.NodeID)
}

func TestParseTraceeJSON_InvalidJSON(t *testing.T) {
	var ev Event
	require.Error(t, ParseTraceeJSON([]byte("not json"), &ev))
}

func TestParseTraceeJSON_RawIsImmutableCopy(t *testing.T) {
	// Mutating the original slice must not affect ev.Raw.
	line := []byte(execveJSON)
	var ev Event
	require.NoError(t, ParseTraceeJSON(line, &ev))

	line[0] = 'X'
	require.Equal(t, byte('{'), ev.Raw[0], "ev.Raw should be a copy, not a slice of the input")
}
