package agentexec

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestClaimWaitsForEphemeralInputHydration(t *testing.T) {
	var n atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(controlplane.Run{ID: "same-run", Inputs: map[string]string{"flag": "transient"}})
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	run, err := claim(ctx, Options{APIURL: s.URL, ID: "agent"}, "same-run")
	require.NoError(t, err)
	require.Equal(t, "transient", run.Inputs["flag"])
	require.EqualValues(t, 2, n.Load())
}
func TestClaimHydrationWaitHonorsCancellation(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := claim(ctx, Options{APIURL: s.URL, ID: "agent"}, "same-run")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
