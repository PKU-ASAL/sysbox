package api

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceDeleteRetainsPinnedRevisionAndSnapshot(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	w := s.workspaceService()
	files := map[string][]byte{"field.sysbox.hcl": []byte("root"), "modules/main.hcl": []byte("module")}
	require.NoError(t, w.UpsertProject(context.Background(), "lab", files))
	pinned := w.HCLFile("lab")
	lockBefore, err := os.Stat(filepath.Join(w.runsDir, ".workspace-locks", "lab.lock"))
	require.NoError(t, err)
	snapshot, err := w.MaterializeWorkspaceSnapshot("lab", "run")
	require.NoError(t, err)
	require.NoError(t, s.apiStore.SaveRun(context.Background(), controlplane.Run{ID: "history", Topology: "lab", Status: controlplane.RunDone, QueuedAt: time.Now().UTC(), EndedAt: time.Now().UTC()}))
	require.NoError(t, w.Delete(context.Background(), "lab", false))
	for _, path := range []string{pinned, snapshot} {
		body, err := os.ReadFile(path)
		require.NoError(t, err, "metadata deletion must not GC a pinned tree")
		require.Equal(t, "root", string(body))
		body, err = os.ReadFile(filepath.Join(filepath.Dir(path), "modules/main.hcl"))
		require.NoError(t, err)
		require.Equal(t, "module", string(body))
	}
	require.NoDirExists(t, filepath.Join(w.workspacesDir, "lab"))
	require.NoFileExists(t, filepath.Join(w.runsDir, "lab", "runs.jsonl"))
	require.NoError(t, w.UpsertProject(context.Background(), "lab", files))
	require.FileExists(t, w.HCLFile("lab"))
	lockAfter, err := os.Stat(filepath.Join(w.runsDir, ".workspace-locks", "lab.lock"))
	require.NoError(t, err)
	require.True(t, os.SameFile(lockBefore, lockAfter), "deletion must not replace the lock inode")
}

// A separate process holds the real filesystem lock, exercising cancellation
// across service instances rather than substituting an in-memory mutex.
func TestWorkspacePublicationLockProcess(t *testing.T) {
	path := os.Getenv("SYSBOX_TEST_WORKSPACE_LOCK")
	if path == "" {
		return
	}
	lease := flock.New(path)
	require.NoError(t, lease.Lock())
	defer lease.Unlock()
	fmt.Println("locked")
	var release [1]byte
	_, _ = os.Stdin.Read(release[:])
}

func TestWorkspacePublicationWaitHonorsCancellationAcrossProcesses(t *testing.T) {
	w := NewServer(t.TempDir(), t.TempDir()).workspaceService()
	files := map[string][]byte{"field.sysbox.hcl": []byte("old")}
	require.NoError(t, w.UpsertProject(context.Background(), "lab", files))
	pinned := w.HCLFile("lab")
	lockPath := filepath.Join(w.runsDir, ".workspace-locks", "lab.lock")
	require.NoError(t, os.MkdirAll(filepath.Dir(lockPath), 0700))
	cmd := exec.Command(os.Args[0], "-test.run=^TestWorkspacePublicationLockProcess$")
	cmd.Env = append(os.Environ(), "SYSBOX_TEST_WORKSPACE_LOCK="+lockPath)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = stdin.Close(); require.NoError(t, cmd.Wait()) })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "locked\n", line)
	for _, operation := range []func(context.Context) error{
		func(ctx context.Context) error {
			return w.UpsertProject(ctx, "lab", map[string][]byte{"field.sysbox.hcl": []byte("new")})
		},
		func(ctx context.Context) error { return w.Delete(ctx, "lab", false) },
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		err := operation(ctx)
		cancel()
		require.ErrorIs(t, err, context.DeadlineExceeded)
	}
	require.Equal(t, pinned, w.HCLFile("lab"))
	require.FileExists(t, pinned)
	// The context-free public revision API must use the same lock too.
	done := make(chan error, 1)
	go func() {
		_, err := w.MaterializeRevision("lab", controlplane.ComputeRevisionDigest(files), files)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("revision materialization bypassed another process's lock: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	require.NoError(t, stdin.Close())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("revision materialization did not resume after unlocking")
	}
}

func TestWorkspaceDeleteOrdersConcurrentPublication(t *testing.T) {
	w := NewServer(t.TempDir(), t.TempDir()).workspaceService()
	require.NoError(t, w.UpsertProject(context.Background(), "lab", map[string][]byte{"field.sysbox.hcl": []byte("old")}))
	pinned := w.HCLFile("lab")
	manager := w.stateManager
	other := newWorkspaceService(w.runsDir, w.workspacesDir, "", manager, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	w.stateManager = func(topology string) (*state.Manager, error) {
		close(entered)
		<-release
		return manager(topology)
	}
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	deleted, published := make(chan error, 1), make(chan error, 1)
	go func() { deleted <- w.Delete(context.Background(), "lab", false) }()
	<-entered
	go func() {
		published <- other.UpsertProject(context.Background(), "lab", map[string][]byte{"field.sysbox.hcl": []byte("new")})
	}()
	select {
	case err := <-published:
		t.Fatalf("publication bypassed an in-progress delete: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	unblock()
	require.NoError(t, <-deleted)
	require.NoError(t, <-published)
	current, err := other.HCL("lab")
	require.NoError(t, err)
	require.Equal(t, "new", string(current))
	old, err := os.ReadFile(pinned)
	require.NoError(t, err)
	require.Equal(t, "old", string(old))
}

func TestUpsertProjectMaterializesWholeTree(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	files := map[string][]byte{
		"field.sysbox.hcl":     []byte("root"),
		"modules/web/main.hcl": []byte("module"),
		"files/run.txt":        []byte("file"),
	}
	require.NoError(t, s.workspaceService().UpsertProject(context.Background(), "proj", files))

	base := filepath.Dir(s.workspaceService().HCLFile("proj"))
	for p, want := range files {
		got, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(p)))
		require.NoError(t, err)
		require.Equal(t, string(want), string(got), "content mismatch at %s", p)
	}
}

func TestUpsertProjectRejectsTraversal(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	for _, p := range []string{"../evil", "a/../../evil", "/abs", `a\b`, "", "a//b", ".", "..", "a/./b", "a/../b"} {
		err := s.workspaceService().UpsertProject(context.Background(), "proj", map[string][]byte{p: []byte("x")})
		require.Error(t, err, "path %q must be rejected", p)
	}
}

func TestUpsertProjectKeepsPreviousTreeWhenStagingFails(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	service := s.workspaceService()
	require.NoError(t, service.UpsertProject(context.Background(), "proj", map[string][]byte{
		"field.sysbox.hcl": []byte("stable"),
	}))

	// The first file makes "nested" a regular file; the second path then
	// fails while creating a directory under that file. The live workspace
	// must remain untouched because publication has not started yet.
	err := service.UpsertProject(context.Background(), "proj", map[string][]byte{
		"field.sysbox.hcl": []byte("replacement"),
		"nested":           []byte("file"),
		"nested/child.hcl": []byte("child"),
	})
	require.Error(t, err)
	got, readErr := os.ReadFile(service.HCLFile("proj"))
	require.NoError(t, readErr)
	require.Equal(t, "stable", string(got))
}

func TestUpsertProjectPinsReaderToPublishedRevision(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir()).workspaceService()
	require.NoError(t, s.UpsertProject(context.Background(), "proj", map[string][]byte{
		"field.sysbox.hcl": []byte("old"), "module/file": []byte("old"),
	}))
	pinned := s.HCLFile("proj")
	require.NoError(t, s.UpsertProject(context.Background(), "proj", map[string][]byte{
		"field.sysbox.hcl": []byte("new"), "module/file": []byte("new"),
	}))
	old, err := os.ReadFile(pinned)
	require.NoError(t, err)
	require.Equal(t, "old", string(old))
	module, err := os.ReadFile(filepath.Join(filepath.Dir(pinned), "module/file"))
	require.NoError(t, err)
	require.Equal(t, "old", string(module))
	current, err := s.HCL("proj")
	require.NoError(t, err)
	require.Equal(t, "new", string(current))
	snapshot, err := s.MaterializeWorkspaceSnapshot("proj", "run-1")
	require.NoError(t, err)
	body, err := os.ReadFile(snapshot)
	require.NoError(t, err)
	require.Equal(t, "new", string(body))
	items, err := s.List(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.True(t, items[0].HasHCL)
}

func TestUpsertProjectConcurrentReadersSeeCompleteRevision(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir()).workspaceService()
	files := func(v string) map[string][]byte {
		return map[string][]byte{"field.sysbox.hcl": []byte(v), "module/file": []byte(v)}
	}
	require.NoError(t, s.UpsertProject(context.Background(), "proj", files("initial")))
	var wg sync.WaitGroup
	for _, value := range []string{"a", "b", "c", "d"} {
		wg.Go(func() {
			for range 10 {
				if err := s.UpsertProject(context.Background(), "proj", files(value)); err != nil {
					t.Error(err)
					return
				}
				pinned := s.HCLFile("proj")
				root, err := os.ReadFile(pinned)
				if err != nil {
					t.Error(err)
					return
				}
				module, err := os.ReadFile(filepath.Join(filepath.Dir(pinned), "module/file"))
				if err != nil {
					t.Error(err)
					return
				}
				if string(root) != string(module) {
					t.Errorf("mixed tree: root=%q module=%q", root, module)
				}
			}
		})
	}
	wg.Wait()
}

func TestUpsertProjectCancelledPublishKeepsCurrent(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir()).workspaceService()
	require.NoError(t, s.UpsertProject(context.Background(), "proj", map[string][]byte{"field.sysbox.hcl": []byte("old")}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, s.UpsertProject(ctx, "proj", map[string][]byte{"field.sysbox.hcl": []byte("new")}), context.Canceled)
	got, err := s.HCL("proj")
	require.NoError(t, err)
	require.Equal(t, "old", string(got))
}

func TestUpsertProjectFailedPointerPublishKeepsPinnedRevision(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir()).workspaceService()
	require.NoError(t, s.UpsertProject(context.Background(), "proj", map[string][]byte{"field.sysbox.hcl": []byte("old")}))
	pinned := s.HCLFile("proj")
	current := filepath.Join(s.workspacesDir, "proj", ".current")
	require.NoError(t, os.Remove(current))
	require.NoError(t, os.Mkdir(current, 0700))
	err := s.UpsertProject(context.Background(), "proj", map[string][]byte{"field.sysbox.hcl": []byte("new")})
	require.Error(t, err)
	old, err := os.ReadFile(pinned)
	require.NoError(t, err)
	require.Equal(t, "old", string(old))
}

func TestValidateRelPath(t *testing.T) {
	for _, p := range []string{"field.sysbox.hcl", "modules/web/main.hcl", "files/run.txt", "a.b/c-d_e"} {
		require.NoError(t, validateRelPath(p), "path %q must be accepted", p)
	}
	for _, p := range []string{"", "../evil", "a/../../evil", "/etc/passwd", `a\b`, "a//b", ".", "..", "a/./b", "a/../b"} {
		require.Error(t, validateRelPath(p), "path %q must be rejected", p)
	}
}
