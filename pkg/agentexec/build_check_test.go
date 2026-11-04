package agentexec

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/buildinfo"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/stretchr/testify/require"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAPIBuildCheck(t *testing.T) {
	local := buildinfo.Info{Version: "v0.1.0", Commit: strings.Repeat("a", 40)}
	for _, tc := range []struct {
		name     string
		peer     buildinfo.Info
		protocol string
		status   int
		ok       bool
	}{
		{"match", local, controlplane.AgentProtocolVersion, 200, true},
		{"different commit", buildinfo.Info{Version: local.Version, Commit: strings.Repeat("b", 40)}, controlplane.AgentProtocolVersion, 200, false},
		{"old API", buildinfo.Info{}, "", 200, false},
		{"unavailable", local, controlplane.AgentProtocolVersion, 503, false},
		{"protocol", local, "other", 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/v1/health", r.URL.Path)
				require.Empty(t, r.Header.Get("Authorization"))
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"build": tc.peer, "agent_protocol": tc.protocol})
			}))
			defer server.Close()
			err := checkAPIBuild(context.Background(), Options{APIURL: server.URL}, local)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
func TestAPIBuildCheckRejectsRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://invalid.example", 307) }))
	defer server.Close()
	require.ErrorContains(t, checkAPIBuild(context.Background(), Options{APIURL: server.URL}, buildinfo.Current()), "HTTP 307")
}

func TestStartupBuildMismatchStopsBeforeHostObservationAndRegistration(t *testing.T) {
	for _, mismatch := range []string{"host", "api"} {
		t.Run(mismatch, func(t *testing.T) {
			dir := t.TempDir()
			public, _, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			publicPath := filepath.Join(dir, "host.pub")
			require.NoError(t, os.WriteFile(publicPath, public, 0600))
			socket := filepath.Join(dir, "host.sock")
			listener, err := net.Listen("unix", socket)
			require.NoError(t, err)
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, e := listener.Accept()
				if e != nil {
					return
				}
				defer conn.Close()
				var request hostsupervisor.Request
				if json.NewDecoder(conn).Decode(&request) != nil {
					return
				}
				if request.Operation != "version" {
					t.Errorf("unexpected operation before build check: %s", request.Operation)
				}
				info := buildinfo.Current()
				if mismatch == "host" {
					info.Commit = strings.Repeat("b", 40)
				}
				_ = json.NewEncoder(conn).Encode(hostsupervisor.Response{Version: 1, BuildVersion: info.Version, BuildCommit: info.Commit})
			}()
			var registrations atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					registrations.Add(1)
				}
				info := buildinfo.Current()
				if mismatch == "api" {
					info.Commit = strings.Repeat("b", 40)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"build": info, "agent_protocol": controlplane.AgentProtocolVersion})
			}))
			defer api.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err = Run(ctx, Options{APIURL: api.URL, ID: "test", HostSupervisorSocket: socket, HostPublicKeyFile: publicPath})
			require.ErrorContains(t, err, "build mismatch")
			require.Zero(t, registrations.Load())
			<-done
		})
	}
}
