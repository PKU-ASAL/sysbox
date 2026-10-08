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

func TestClaimRetriesLostResponseWithSameExecutionIdentity(t *testing.T) {
	var calls atomic.Int32
	var identity string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ClaimID string `json:"claim_id"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.NotEmpty(t, request.ClaimID)
		if calls.Add(1) == 1 {
			identity = request.ClaimID
			connection, _, err := w.(http.Hijacker).Hijack()
			require.NoError(t, err)
			connection.Close()
			return
		}
		require.Equal(t, identity, request.ClaimID)
		json.NewEncoder(w).Encode(controlplane.Run{ID: "same-run", LeaseOwner: identity})
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	run, err := claim(ctx, Options{APIURL: s.URL, ID: "agent"}, "same-run")
	require.NoError(t, err)
	require.Equal(t, identity, run.LeaseOwner)
	require.EqualValues(t, 2, calls.Load())
}

func TestClaimRetriesTruncatedReceipt(t *testing.T) {
	var n atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Write([]byte(`{"id":`))
			return
		}
		json.NewEncoder(w).Encode(controlplane.Run{ID: "run"})
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	run, err := claim(ctx, Options{APIURL: s.URL, ID: "agent"}, "run")
	require.NoError(t, err)
	require.Equal(t, "run", run.ID)
	require.EqualValues(t, 2, n.Load())
}
func TestClaimDoesNotRetryProtocolRejection(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 409} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var n atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n.Add(1); w.WriteHeader(status) }))
			defer s.Close()
			_, err := claim(context.Background(), Options{APIURL: s.URL, ID: "agent"}, "run")
			require.Error(t, err)
			require.EqualValues(t, 1, n.Load())
		})
	}
}
