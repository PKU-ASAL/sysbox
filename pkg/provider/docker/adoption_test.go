package docker

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestExplicitAdoptionBindsImmutableContainerIncarnation(t *testing.T) {
	fullID := strings.Repeat("a", 64)
	created := "2026-10-07T00:00:00Z"
	s := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.URL.Path, "/containers/"+fullID+"/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"Id": fullID, "Created": created, "Image": "sha256:image", "Name": "/external", "Config": map[string]any{"Labels": map[string]string{}}, "NetworkSettings": map[string]any{}}))
	})
	s.adoptionRoot = t.TempDir()
	ctx := substrate.WithOperationIdentity(context.Background(), testOperation())
	evidence, err := s.InspectAdoption(ctx, fullID, substrate.NodeSpec{Image: substrate.ArtifactHandle{ID: "sha256:image"}})
	require.NoError(t, err)
	handle := substrate.NodeHandle{ID: fullID, Generation: testOperation().Generation}
	require.Error(t, s.validateManagedHandle(ctx, &handle), "inspection cannot grant ownership")
	adopted, err := s.ApplyAdoption(ctx, evidence)
	require.NoError(t, err)
	require.Equal(t, fullID, adopted.ID)
	require.NoError(t, s.validateManagedHandle(ctx, &adopted))
	_, err = s.ApplyAdoption(ctx, evidence)
	require.NoError(t, err, "exact retry must be idempotent")
	foreign := testOperation()
	foreign.OwnerUID = "foreign"
	_, err = s.ObserveAdoption(substrate.WithOperationIdentity(context.Background(), foreign), evidence)
	require.Error(t, err)
	created = "2026-10-08T00:00:00Z"
	require.Error(t, s.validateManagedHandle(ctx, &adopted), "same ID with altered creation fingerprint is not owned")
}

func TestConcurrentAdoptionCannotStealOwnership(t *testing.T) {
	id := strings.Repeat("b", 64)
	s := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"Id": id, "Created": "now", "Image": "sha256:image", "Config": map[string]any{"Labels": map[string]string{}}})
	})
	s.adoptionRoot = t.TempDir()
	evidence, err := s.InspectAdoption(context.Background(), id, substrate.NodeSpec{})
	require.NoError(t, err)
	results := make(chan error, 2)
	var start sync.WaitGroup
	start.Add(1)
	for _, owner := range []string{"one", "two"} {
		go func(owner string) {
			start.Wait()
			identity := testOperation()
			identity.OwnerUID = owner
			_, err := s.ApplyAdoption(substrate.WithOperationIdentity(context.Background(), identity), evidence)
			results <- err
		}(owner)
	}
	start.Done()
	success := 0
	for range 2 {
		if <-results == nil {
			success++
		}
	}
	require.Equal(t, 1, success)
}

func TestAdoptionRejectsForeignLabelsAndUnsafeIDs(t *testing.T) {
	id := strings.Repeat("c", 64)
	s := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"Id": id, "Created": "now", "Image": "sha256:image", "Config": map[string]any{"Labels": map[string]string{"sysbox.owner_uid": "foreign"}}})
	})
	s.adoptionRoot = t.TempDir()
	ctx := substrate.WithOperationIdentity(context.Background(), testOperation())
	evidence, err := s.InspectAdoption(ctx, id, substrate.NodeSpec{})
	require.NoError(t, err)
	_, err = s.ApplyAdoption(ctx, evidence)
	require.ErrorContains(t, err, "conflicting ownership")
	evidence.ExternalID = "../../foreign"
	_, err = s.ApplyAdoption(ctx, evidence)
	require.Error(t, err)
}
