package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/stretchr/testify/require"
)

func TestNonDockerGCCancellationAndCorruptJournalNeverReplay(t *testing.T) {
	for _, kind := range []string{"domain", "firecracker"} {
		t.Run(kind, func(t *testing.T) {
			c, candidate, _ := gcFixture(t)
			candidate.Kind = kind
			pub, key, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			c.IssuerKey = pub
			digest, err := candidate.Digest()
			require.NoError(t, err)
			grant, err := controlplane.SignExecutionGrant(key, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: candidate.TopologyUID, ResourceUID: candidate.ResourceUID, Generation: 1}, AgentID: c.AgentID, SessionID: "session", ActionID: "gc", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "gc_delete", Digest: digest, TargetBootID: c.HostBootID, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}, c.HostBootID, time.Now())
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			entered, release := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() {
				_, err := c.Delete(ctx, candidate, grant, func(context.Context) (json.RawMessage, error) {
					close(entered)
					<-release
					return json.RawMessage(`{"deleted":true}`), nil
				})
				done <- err
			}()
			<-entered
			cancel()
			blocked, stop := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer stop()
			_, err = c.Delete(blocked, candidate, grant, func(context.Context) (json.RawMessage, error) {
				t.Error("uncancellable first effect still in flight")
				return nil, nil
			})
			require.Error(t, err)
			close(release)
			require.ErrorIs(t, <-done, effectgate.ErrUncertain)
			_, err = c.Observe(context.Background(), candidate, grant)
			require.ErrorIs(t, err, effectgate.ErrUncertain)
			path := filepath.Join(c.Root, fmt.Sprintf("%x.json", sha256.Sum256([]byte(candidate.TopologyUID))))
			require.NoError(t, os.WriteFile(path, []byte(`{"broken":`), 0600))
			_, err = c.Delete(context.Background(), candidate, grant, func(context.Context) (json.RawMessage, error) {
				t.Error("corrupt journal must not replay")
				return nil, nil
			})
			require.Error(t, err)
		})
	}
}
func TestNonDockerGCOutcomePersistenceFailureNeverReportsSuccess(t *testing.T) {
	for _, kind := range []string{"domain", "firecracker"} {
		t.Run(kind, func(t *testing.T) {
			c, candidate, _ := gcFixture(t)
			candidate.Kind = kind
			pub, key, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			c.IssuerKey = pub
			_, hostKey, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			c.HostKey = hostKey
			digest, err := candidate.Digest()
			require.NoError(t, err)
			grant, err := controlplane.SignExecutionGrant(key, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: candidate.TopologyUID, ResourceUID: candidate.ResourceUID, Generation: 1}, AgentID: c.AgentID, SessionID: "session", ActionID: "gc", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "gc_delete", Digest: digest, TargetBootID: c.HostBootID, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}, c.HostBootID, time.Now())
			require.NoError(t, err)
			reports := 0
			c.ReportStop = func(context.Context, controlplane.SignedExecutionStopReceipt) error { reports++; return nil }
			path := filepath.Join(c.Root, fmt.Sprintf("%x.json", sha256.Sum256([]byte(candidate.TopologyUID))))
			// Preserve the original durable intent, make atomic outcome rename fail,
			// then restore it exactly to model publication failure without erasing proof.
			backup := path + ".original"
			_, err = c.Delete(context.Background(), candidate, grant, func(context.Context) (json.RawMessage, error) {
				require.NoError(t, os.Rename(path, backup))
				require.NoError(t, os.Mkdir(path, 0700))
				return json.RawMessage(`{"deleted":true}`), nil
			})
			require.Error(t, err)
			require.Zero(t, reports)
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.Rename(backup, path))
			_, err = c.Observe(context.Background(), candidate, grant)
			require.ErrorIs(t, err, effectgate.ErrUncertain)
			_, err = c.Delete(context.Background(), candidate, grant, func(context.Context) (json.RawMessage, error) {
				t.Error("failed outcome publication must not replay")
				return nil, nil
			})
			require.ErrorIs(t, err, effectgate.ErrUncertain)
		})
	}
}

func TestGCMissingJournalWithAdmissionEvidenceCannotReplay(t *testing.T) {
	c, candidate, g := gcFixture(t)
	_, err := c.Delete(context.Background(), candidate, g, func(context.Context) (json.RawMessage, error) { return nil, fmt.Errorf("lost reply") })
	require.ErrorIs(t, err, effectgate.ErrUncertain)
	path := filepath.Join(c.Root, fmt.Sprintf("%x.json", sha256.Sum256([]byte(candidate.TopologyUID))))
	require.NoError(t, os.Remove(path))
	called := false
	_, err = c.Delete(context.Background(), candidate, g, func(context.Context) (json.RawMessage, error) {
		called = true
		return json.RawMessage(`{"deleted":true}`), nil
	})
	require.Error(t, err)
	require.False(t, called, "lost journal is not proof effect never started")
}

func TestGCJournalLossWhileWaitingCannotReplay(t *testing.T) {
	c, candidate, grant := gcFixture(t)
	path := filepath.Join(c.Root, fmt.Sprintf("%x.json", sha256.Sum256([]byte(candidate.TopologyUID))))
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := c.Delete(context.Background(), candidate, grant, func(context.Context) (json.RawMessage, error) {
			close(entered)
			<-release
			if err := os.Remove(path); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("lost journal and reply")
		})
		done <- err
	}()
	<-entered
	second := make(chan error, 1)
	replayed := false
	go func() {
		_, err := c.Delete(context.Background(), candidate, grant, func(context.Context) (json.RawMessage, error) {
			replayed = true
			return json.RawMessage(`{"deleted":true}`), nil
		})
		second <- err
	}()
	// Keep first callback in flight while the second caller queues on its lock.
	time.Sleep(100 * time.Millisecond)
	close(release)
	require.ErrorIs(t, <-done, effectgate.ErrUncertain)
	require.ErrorIs(t, <-second, effectgate.ErrUncertain)
	require.False(t, replayed)
}
