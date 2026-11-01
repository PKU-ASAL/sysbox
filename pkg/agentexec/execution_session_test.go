package agentexec

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

type grantRoundTripper func(*http.Request) (*http.Response, error)

func (f grantRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestExecutionSessionRecoversLostRegistrationBeforeNewIncarnation(t *testing.T) {
	current := controlplane.AgentSession{}
	fail := true
	count := 0
	client := &http.Client{Transport: grantRoundTripper(func(r *http.Request) (*http.Response, error) {
		var req struct {
			Incarnation string `json:"incarnation_id"`
			Expected    string `json:"expected_session"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		if current.IncarnationID != req.Incarnation {
			require.Equal(t, current.ID, req.Expected)
			count++
			current = controlplane.AgentSession{ID: strings.Repeat("s", count), AgentID: "agent", IncarnationID: req.Incarnation}
		}
		raw, _ := json.Marshal(current)
		status := 200
		if fail {
			status = 503
			fail = false
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(raw))), Header: http.Header{}}, nil
	})}
	opts := Options{ID: "agent", APIURL: "http://api", Secret: "secret", ExecutionSessionFile: filepath.Join(t.TempDir(), "session.json"), httpClient: client}
	_, err := establishExecutionSession(context.Background(), opts)
	require.Error(t, err)
	session, err := establishExecutionSession(context.Background(), opts)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.Equal(t, current.ID, session.ID)
}
