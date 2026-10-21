package revisiongate

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemoteConsumerPinsBeforePublicationAndRetirementFencesReplay(t *testing.T) {
	ctx := context.Background()
	registry := t.TempDir()
	consumer := t.TempDir()
	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("test-secret"), 0600))
	service, err := NewRemoteRegistry(registry, map[string]string{"worker": tokenFile})
	require.NoError(t, err)
	server := httptest.NewServer(service)
	defer server.Close()
	require.NoError(t, BindRemote(ctx, consumer, RemoteBinding{URL: server.URL, Consumer: "worker", TokenFile: tokenFile}))
	id := "sha256:" + strings.Repeat("a", 64)
	raw, _ := json.Marshal(map[string]string{"revision": id})
	release, err := Guard(ctx, filepath.Join(consumer, "lab", "record.json"), raw)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(consumer, "lab"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(consumer, "lab", "record.json"), raw, 0600))
	release()
	unlock, err := Lock(ctx, registry)
	require.NoError(t, err)
	require.Error(t, ValidateRemoteConsumers(registry, id, time.Now()))
	unlock()
	require.NoError(t, os.Remove(filepath.Join(consumer, "lab", "record.json")))
	require.NoError(t, SyncRemote(ctx, consumer))
	unlock, err = Lock(ctx, registry)
	require.NoError(t, err)
	require.NoError(t, ValidateRemoteConsumers(registry, id, time.Now()))
	require.NoError(t, RetireBatch(registry, []string{id}))
	unlock()
	_, err = Guard(ctx, filepath.Join(consumer, "lab", "record.json"), raw)
	require.Error(t, err)
	require.Error(t, ValidateRemoteConsumers(registry, "sha256:"+strings.Repeat("b", 64), time.Now().Add(2*time.Hour)))
}
func TestRemoteConsumerAuthAndUnavailableNeverAdmit(t *testing.T) {
	registry := t.TempDir()
	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("test-secret"), 0600))
	service, err := NewRemoteRegistry(registry, map[string]string{"worker": tokenFile})
	require.NoError(t, err)
	server := httptest.NewServer(service)
	response, err := http.Post(server.URL+"/worker/pins", "application/json", strings.NewReader(`{"references":[]}`))
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, 401, response.StatusCode)
	root := t.TempDir()
	require.NoError(t, BindRemote(context.Background(), root, RemoteBinding{URL: server.URL, Consumer: "worker", TokenFile: tokenFile}))
	server.Close()
	_, err = Guard(context.Background(), filepath.Join(root, "record.json"), []byte(`{"revision":"sha256:`+strings.Repeat("a", 64)+`"}`))
	require.Error(t, err)
}

func TestRemoteDelayedReleaseCannotOverwriteNewerPin(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(key, []byte("test-secret"), 0600))
	service, err := NewRemoteRegistry(root, map[string]string{"worker": key})
	require.NoError(t, err)
	post := func(body string) int {
		r := httptest.NewRequest("POST", "/worker/pins", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer test-secret")
		w := httptest.NewRecorder()
		service.ServeHTTP(w, r)
		return w.Code
	}
	id := "sha256:" + strings.Repeat("a", 64)
	require.Equal(t, 200, post(`{"version":1,"incarnation":"host","sequence":2,"references":["`+id+`"],"replace":true}`))
	require.Equal(t, 409, post(`{"version":1,"incarnation":"host","sequence":1,"references":[],"replace":true}`))
	require.Error(t, ValidateRemoteConsumers(root, id, time.Now()))
}
func TestRemoteMissingEnrollmentNeverMeansEmpty(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(key, []byte("test-secret"), 0600))
	_, err := NewRemoteRegistry(root, map[string]string{"worker": key})
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(root, remoteConsumersFile)))
	require.Error(t, ValidateRemoteConsumers(root, "sha256:"+strings.Repeat("a", 64), time.Now()))
	_, err = NewRemoteRegistry(root, map[string]string{"other": key})
	require.Error(t, err)
}

func TestRemoteSyncRetainsMaterializedRevisionDirectory(t *testing.T) {
	ctx := context.Background()
	registry, root := t.TempDir(), t.TempDir()
	key := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(key, []byte("test-secret"), 0600))
	service, err := NewRemoteRegistry(registry, map[string]string{"worker": key})
	require.NoError(t, err)
	server := httptest.NewServer(service)
	defer server.Close()
	require.NoError(t, BindRemote(ctx, root, RemoteBinding{URL: server.URL, Consumer: "worker", TokenFile: key}))
	id := "sha256:" + strings.Repeat("c", 64)
	path := filepath.Join(root, "lab", "revisions", id)
	require.NoError(t, os.MkdirAll(path, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(path, "field.sysbox.hcl"), []byte("# retained"), 0600))
	require.NoError(t, SyncRemote(ctx, root))
	require.Error(t, ValidateRemoteConsumers(registry, id, time.Now()))
}

func TestRemoteSyncRejectsTruncatedJSONWithoutReleasingPins(t *testing.T) {
	ctx := context.Background()
	registry, root := t.TempDir(), t.TempDir()
	key := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(key, []byte("test-secret"), 0600))
	service, err := NewRemoteRegistry(registry, map[string]string{"worker": key})
	require.NoError(t, err)
	server := httptest.NewServer(service)
	defer server.Close()
	require.NoError(t, BindRemote(ctx, root, RemoteBinding{URL: server.URL, Consumer: "worker", TokenFile: key}))
	id := "sha256:" + strings.Repeat("c", 64)
	path := filepath.Join(root, "checkpoint.json")
	raw := []byte(`{"revision":"` + id + `"}`)
	unlock, err := Guard(ctx, path, raw)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0600))
	unlock()
	require.NoError(t, os.WriteFile(path, []byte("  \n"), 0600))
	require.Error(t, SyncRemote(ctx, root))
	require.Error(t, ValidateRemoteConsumers(registry, id, time.Now()))
}

func TestRemoteMissingCatalogCannotBypassPinAdmission(t *testing.T) {
	ctx := context.Background()
	registry, root := t.TempDir(), t.TempDir()
	key := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(key, []byte("test-secret"), 0600))
	service, err := NewRemoteRegistry(registry, map[string]string{"worker": key})
	require.NoError(t, err)
	server := httptest.NewServer(service)
	defer server.Close()
	require.NoError(t, BindRemote(ctx, root, RemoteBinding{URL: server.URL, Consumer: "worker", TokenFile: key}))
	require.NoError(t, os.Rename(filepath.Join(root, "global-revisions"), filepath.Join(t.TempDir(), "lost-catalog")))
	_, err = Guard(ctx, filepath.Join(root, "lab", "record.json"), []byte(`{"revision":"sha256:`+strings.Repeat("a", 64)+`"}`))
	require.Error(t, err)
}

func TestRemoteConsumerCannotSilentlyReenrollAfterBindingLoss(t *testing.T) {
	ctx := context.Background()
	registry, root := t.TempDir(), t.TempDir()
	key := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(key, []byte("test-secret"), 0600))
	service, err := NewRemoteRegistry(registry, map[string]string{"worker": key})
	require.NoError(t, err)
	server := httptest.NewServer(service)
	defer server.Close()
	binding := RemoteBinding{URL: server.URL, Consumer: "worker", TokenFile: key}
	require.NoError(t, BindRemote(ctx, root, binding))
	require.NoError(t, os.Remove(filepath.Join(root, "global-revisions", remoteBindingFile)))
	require.Error(t, BindRemote(ctx, root, binding))
	_, err = Guard(ctx, filepath.Join(root, "record.json"), []byte(`{"revision":"sha256:`+strings.Repeat("a", 64)+`"}`))
	require.Error(t, err)
}

func TestExplicitCatalogGuardCannotBypassLostRemoteBinding(t *testing.T) {
	ctx := context.Background()
	registry, root := t.TempDir(), t.TempDir()
	key := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(key, []byte("test-secret"), 0600))
	service, err := NewRemoteRegistry(registry, map[string]string{"worker": key})
	require.NoError(t, err)
	server := httptest.NewServer(service)
	defer server.Close()
	require.NoError(t, BindRemote(ctx, root, RemoteBinding{URL: server.URL, Consumer: "worker", TokenFile: key}))
	catalog := filepath.Join(root, "global-revisions")
	require.NoError(t, os.Rename(catalog, filepath.Join(t.TempDir(), "lost")))
	require.NoError(t, os.Mkdir(catalog, 0700))
	_, err = GuardCatalog(ctx, catalog, []byte(`{"revision":"sha256:`+strings.Repeat("a", 64)+`"}`))
	require.Error(t, err)
}
