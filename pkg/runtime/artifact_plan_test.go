package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/substrate"
)

type planArtifactNoRuntimeDriver struct{ substrate.BaseSubstrate }

func (planArtifactNoRuntimeDriver) ResolveImage(context.Context, substrate.ArtifactSource) (substrate.ArtifactHandle, error) {
	return substrate.ArtifactHandle{}, errors.New("provider runtime must not be called while planning")
}

func TestResolvePlanArtifactDigestsDoesNotTouchOCIRuntime(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "test", Artifact: planArtifactNoRuntimeDriver{}}))
	g := graph.New()
	image := address.Resource("sysbox_image", "service")
	require.NoError(t, g.AddNode(image, nil))
	require.NoError(t, g.SetData(image, &config.ImageConfig{Substrate: "docker", Kind: "oci", Source: "python:3.13-bookworm", Architecture: "amd64", GuestFamily: "linux"}))

	digests, err := ResolvePlanArtifactDigests(context.Background(), g)

	require.NoError(t, err)
	require.Regexp(t, `^sha256:[0-9a-f]{64}$`, digests[image.String()])
}

func TestResolvePlanArtifactDigestsDetectsLocalImageMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rootfs.ext4")
	require.NoError(t, os.WriteFile(path, []byte("first"), 0o644))
	g := graph.New()
	image := address.Resource("sysbox_image", "rootfs")
	require.NoError(t, g.AddNode(image, nil))
	require.NoError(t, g.SetData(image, &config.ImageConfig{Substrate: "firecracker", Kind: "rootfs", Source: path, Architecture: "amd64", GuestFamily: "linux"}))

	first, err := ResolvePlanArtifactDigests(context.Background(), g)
	require.NoError(t, err)
	require.Regexp(t, `^sha256:[0-9a-f]{64}$`, first[image.String()])
	require.NoError(t, os.WriteFile(path, []byte("second"), 0o644))
	second, err := ResolvePlanArtifactDigests(context.Background(), g)
	require.NoError(t, err)
	require.NotEqual(t, first, second)
}

func TestResolvePlanPinnedVMArtifactsDoesNotFetch(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected plan download", http.StatusInternalServerError)
	}))
	defer server.Close()

	const imageSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const kernelSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	g := graph.New()
	image := address.Resource("sysbox_image", "rootfs")
	kernel := address.Resource("sysbox_kernel", "boot")
	require.NoError(t, g.AddNode(image, nil))
	require.NoError(t, g.SetData(image, &config.ImageConfig{
		Substrate: "firecracker", Kind: "rootfs", Source: server.URL + "/rootfs.ext4",
		SHA256: imageSHA, Architecture: "amd64", GuestFamily: "linux",
	}))
	require.NoError(t, g.AddNode(kernel, nil))
	require.NoError(t, g.SetData(kernel, &config.KernelConfig{
		Substrate: "firecracker", Source: server.URL + "/vmlinux",
		SHA256: "sha256:" + kernelSHA, Architecture: "amd64",
	}))

	digests, err := ResolvePlanArtifactDigests(context.Background(), g)
	require.NoError(t, err)
	require.Equal(t, "sha256:"+imageSHA, digests[image.String()])
	require.Equal(t, "sha256:"+kernelSHA, digests[kernel.String()])
	require.EqualValues(t, 0, requests.Load())
}

func TestResolvePlanRejectsInvalidPinnedVMArtifact(t *testing.T) {
	g := graph.New()
	image := address.Resource("sysbox_image", "rootfs")
	require.NoError(t, g.AddNode(image, nil))
	require.NoError(t, g.SetData(image, &config.ImageConfig{
		Substrate: "firecracker", Kind: "rootfs", Source: "https://example.invalid/rootfs.ext4",
		SHA256: "bad-pin", Architecture: "amd64", GuestFamily: "linux",
	}))
	_, err := ResolvePlanArtifactDigests(context.Background(), g)
	require.ErrorContains(t, err, "expected SHA-256")
}

func TestResolvePlanLocalFirstVMArtifactDoesNotFetch(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected plan download", http.StatusInternalServerError)
	}))
	defer server.Close()
	localPath := filepath.Join(t.TempDir(), "rootfs.ext4")
	pin := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	g := graph.New()
	image := address.Resource("sysbox_image", "rootfs")
	require.NoError(t, g.AddNode(image, nil))
	require.NoError(t, g.SetData(image, &config.ImageConfig{
		Substrate: "firecracker", Kind: "rootfs", Source: localPath,
		PullSource: server.URL + "/rootfs.ext4", SHA256: pin,
		Architecture: "amd64", GuestFamily: "linux",
	}))
	digests, err := ResolvePlanArtifactDigests(context.Background(), g)
	require.NoError(t, err)
	require.Equal(t, "sha256:"+pin, digests[image.String()])
	require.EqualValues(t, 0, requests.Load())
}
