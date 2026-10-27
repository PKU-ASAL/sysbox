package docker

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeleteManagedNetworkExternalRevalidatesAndRemoves(t *testing.T) {
	removed := false
	sub := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/networks/") {
			if removed {
				http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"Id":"network-1","Labels":{"sysbox.managed":"true","sysbox.owner_uid":"owner","sysbox.topology_uid":"owner","sysbox.resource_uid":"resource","sysbox.generation":"3"}}`))
			return
		}
		if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/networks/network-1") {
			removed = true
			_, _ = w.Write([]byte(`{}`))
			return
		}
		http.Error(w, "unexpected", http.StatusNotFound)
	})
	require.NoError(t, sub.DeleteManagedNetworkExternal(context.Background(), "network-1", "owner", "resource", 3))
	require.True(t, removed)
}

func TestDeleteManagedNetworkExternalRejectsOwnershipDrift(t *testing.T) {
	sub := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"Id":"network-1","Labels":{"sysbox.managed":"true","sysbox.owner_uid":"other","sysbox.topology_uid":"other","sysbox.resource_uid":"resource","sysbox.generation":"3"}}`))
			return
		}
		t.Fatalf("unexpected mutation %s %s", r.Method, r.URL.Path)
	})
	require.Error(t, sub.DeleteManagedNetworkExternal(context.Background(), "network-1", "owner", "resource", 3))
}
