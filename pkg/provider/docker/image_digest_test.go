package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestResolveImageDigestIdentityAndPublication(t *testing.T) {
	config := "sha256:" + strings.Repeat("a", 64)
	manifest := "sha256:" + strings.Repeat("b", 64)
	for _, tc := range []struct {
		name, id, expected, want string
		reject                   bool
	}{
		{"config", config, config, config, false},
		{"manifest-via-repo", config, manifest, manifest, false},
		{"manifest-id", manifest, manifest, manifest, false},
		{"unlocked", config, "", config, false},
		{"unknown", config, "sha256:" + strings.Repeat("c", 64), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			began, completed := false, false
			sub := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
				require.True(t, began, "publication must precede daemon access")
				require.Equal(t, "GET", r.Method)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"Id": tc.id, "RepoDigests": []string{"malformed-entry", "repo/other@sha256:" + strings.Repeat("d", 64), "repo/frr@" + manifest}}))
			})
			ctx := artifact.WithPublication(context.Background(), func(_ context.Context, s artifact.Spec) (func(artifact.Result) error, error) {
				began = true
				require.Equal(t, tc.expected, s.SHA256)
				return func(result artifact.Result) error {
					completed = true
					require.Equal(t, "oci:"+tc.id, result.Path)
					require.Equal(t, strings.TrimPrefix(tc.want, "sha256:"), result.SHA256)
					return nil
				}, nil
			})
			h, err := sub.ResolveImage(ctx, substrate.ArtifactSource{Kind: substrate.ArtifactOCI, Source: "repo/frr", ExpectedDigest: tc.expected, Architecture: "amd64", GuestFamily: substrate.GuestFamilyLinux})
			if tc.reject {
				require.Error(t, err)
				require.False(t, completed)
				return
			}
			require.NoError(t, err)
			require.True(t, completed)
			require.Equal(t, tc.want, h.Identity.Digest)
			require.Equal(t, tc.id, h.ID)
		})
	}
}
func TestResolveImageCannotBypassPublicationFailure(t *testing.T) {
	sentinel := errors.New("publication fenced")
	sub := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("fenced publication reached daemon") })
	ctx := artifact.WithPublication(context.Background(), func(context.Context, artifact.Spec) (func(artifact.Result) error, error) { return nil, sentinel })
	_, err := sub.ResolveImage(ctx, substrate.ArtifactSource{Kind: substrate.ArtifactOCI, Source: "repo/frr"})
	require.ErrorIs(t, err, sentinel)
}

func TestResolveImageCompletionFailureReturnsNoHandle(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	sentinel := errors.New("source publication failed")
	sub := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"Id": id}))
	})
	ctx := artifact.WithPublication(context.Background(), func(context.Context, artifact.Spec) (func(artifact.Result) error, error) {
		return func(artifact.Result) error { return sentinel }, nil
	})
	h, err := sub.ResolveImage(ctx, substrate.ArtifactSource{Kind: substrate.ArtifactOCI, Source: "repo/frr", Architecture: "amd64", GuestFamily: substrate.GuestFamilyLinux})
	require.ErrorIs(t, err, sentinel)
	require.Empty(t, h.ID)
}
