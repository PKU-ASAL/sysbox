//go:build e2e

package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	fc "github.com/oslab/sysbox/pkg/provider/firecracker"
	lv "github.com/oslab/sysbox/pkg/provider/libvirt"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type gcCrashInput struct {
	Kind, Phase, Root, ProviderRoot, Kernel, Ready string
	Candidate                                      controlplane.GCCandidate
	Grant                                          controlplane.SignedExecutionGrant
	Issuer                                         ed25519.PublicKey
	HostKey                                        ed25519.PrivateKey
}

func crashCoordinator(input gcCrashInput) *hostsupervisor.GCCoordinator {
	return &hostsupervisor.GCCoordinator{Root: input.Root, AgentID: "host", HostBootID: "boot", IssuerKey: input.Issuer, HostKey: input.HostKey, Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }, ValidateInventory: func(context.Context, controlplane.GCCandidate) error { return nil }}
}
func TestGCProviderCrashProcess(t *testing.T) {
	path := os.Getenv("SYSBOX_GC_CRASH_INPUT")
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var input gcCrashInput
	require.NoError(t, json.Unmarshal(raw, &input))
	boundary := func() {
		require.NoError(t, os.WriteFile(input.Ready, []byte(input.Phase), 0600))
		select {}
	}
	c := crashCoordinator(input)
	c.ReportStop = func(context.Context, controlplane.SignedExecutionStopReceipt) error { boundary(); return nil }
	_, err = c.Delete(context.Background(), input.Candidate, input.Grant, func(ctx context.Context) (json.RawMessage, error) {
		if input.Phase == "before-effect" {
			boundary()
		}
		var err error
		target := input.Candidate
		if input.Kind == "domain" {
			err = lv.New(input.ProviderRoot).DeleteManagedExternal(ctx, target.ExternalID, target.OwnerUID, target.ResourceUID, target.Generation)
		} else {
			err = fc.New(input.Kernel, input.ProviderRoot).DeleteManagedExternal(ctx, target.ExternalID, target.OwnerUID, target.ResourceUID, target.Generation)
		}
		require.NoError(t, err)
		if input.Phase == "missing-journal" || input.Phase == "corrupt-journal" {
			path := filepath.Join(input.Root, fmt.Sprintf("%x.json", sha256.Sum256([]byte(input.Candidate.TopologyUID))))
			if input.Phase == "missing-journal" {
				require.NoError(t, os.Remove(path))
			} else {
				require.NoError(t, os.WriteFile(path, []byte(`{"broken":`), 0600))
			}
			boundary()
		}
		if input.Phase == "after-effect" {
			boundary()
		}
		return json.RawMessage(`{"deleted":true}`), nil
	})
	t.Fatalf("crash boundary not reached: %v", err)
}
func TestNonDockerGCProcessCrashBoundariesE2E(t *testing.T) {
	for _, kind := range []string{"domain", "firecracker"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "domain" && os.Getenv("SYSBOX_LIBVIRT_E2E") != "1" {
				t.Skip("real libvirt required")
			}
			if kind == "firecracker" {
				for _, name := range []string{"SYSBOX_FIRECRACKER_BIN", "SYSBOX_FIRECRACKER_KERNEL", "SYSBOX_FIRECRACKER_ROOTFS"} {
					if os.Getenv(name) == "" {
						t.Skip("real Firecracker artifacts required")
					}
				}
			}
			for _, phase := range []string{"before-effect", "after-effect", "before-report", "missing-journal", "corrupt-journal"} {
				t.Run(phase, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
					defer cancel()
					private := func() string { p := t.TempDir(); require.NoError(t, os.Chmod(p, 0700)); return p }
					pub, key, err := ed25519.GenerateKey(rand.Reader)
					require.NoError(t, err)
					_, hostKey, err := ed25519.GenerateKey(rand.Reader)
					require.NoError(t, err)
					owner := uuid.NewString()
					providerRoot := private()
					kernel := os.Getenv("SYSBOX_FIRECRACKER_KERNEL")
					var target, survivor string
					var present func(string) bool
					var stopped func(string)
					if kind == "domain" {
						create := func(uid string) string {
							id := substrate.OperationIdentity{OwnerUID: owner, TopologyUID: owner, ResourceUID: uid, Generation: 1, ActionID: "create"}
							external := uuid.NewString()
							name := "gc-crash-" + external
							raw, err := lv.GenerateDomainXML(lv.DomainSpec{Name: name, UUID: external, VCPUs: 1, MemoryMiB: 128, MachineType: "q35", Identity: &id, ManagedState: &lv.HandleState{Identity: &id, DomainName: name, DomainUUID: external}})
							require.NoError(t, err)
							raw = regexp.MustCompile(`(?s)<disk\b.*?</disk>`).ReplaceAllString(raw, "")
							path := filepath.Join(private(), "domain.xml")
							require.NoError(t, os.WriteFile(path, []byte(raw), 0600))
							out, err := exec.CommandContext(ctx, "virsh", "define", path).CombinedOutput()
							require.NoError(t, err, string(out))
							t.Cleanup(func() {
								cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
								defer cancel()
								_, _ = exec.CommandContext(cleanup, "virsh", "destroy", external).CombinedOutput()
								_, _ = exec.CommandContext(cleanup, "virsh", "undefine", external).CombinedOutput()
							})
							return external
						}
						target = create("target")
						survivor = create("survivor")
						present = func(id string) bool {
							raw, err := exec.CommandContext(ctx, "virsh", "list", "--all", "--uuid").CombinedOutput()
							require.NoError(t, err, string(raw))
							for _, s := range strings.Fields(string(raw)) {
								if s == id {
									return true
								}
							}
							return false
						}
					} else {
						cfg := config.DefaultServiceConfig()
						cfg.Providers.Firecracker.Binary = os.Getenv("SYSBOX_FIRECRACKER_BIN")
						raw, err := json.Marshal(cfg)
						require.NoError(t, err)
						path := filepath.Join(private(), "config.json")
						require.NoError(t, os.WriteFile(path, raw, 0600))
						t.Setenv("SYSBOX_CONFIG", path)
						provider := fc.New(kernel, providerRoot)
						handles := map[string]substrate.NodeHandle{}
						proofs := map[string]gcProcessProof{}
						create := func(uid string) string {
							scoped := substrate.WithOperationIdentity(ctx, substrate.OperationIdentity{OwnerUID: owner, TopologyUID: owner, ResourceUID: uid, Generation: 1, ActionID: "create"})
							h, err := provider.CreateNode(scoped, substrate.NodeSpec{Name: uid, Image: substrate.ArtifactHandle{ID: os.Getenv("SYSBOX_FIRECRACKER_ROOTFS")}, ProviderConfig: &fc.Config{Kernel: kernel, AllowDirect: true}})
							require.NoError(t, err)
							t.Cleanup(func() {
								cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
								defer cancel()
								require.NoError(t, provider.DestroyNode(cleanup, h))
							})
							require.NoError(t, provider.StartNode(scoped, h))
							obs, err := provider.ObserveNode(scoped, h)
							require.NoError(t, err)
							proofs[h.ID] = captureGCProcess(t, obs.PID, h.Provider.(*fc.HandleState).Socket)
							handles[h.ID] = h
							return h.ID
						}
						target = create("target")
						survivor = create("survivor")
						stopped = func(id string) { requireGCProcessGone(t, proofs[id]) }
						present = func(id string) bool {
							obs, err := provider.ObserveNode(ctx, handles[id])
							require.NoError(t, err)
							if id == survivor {
								require.True(t, obs.Running)
							}
							return obs.Exists
						}
					}
					candidate := controlplane.GCCandidate{Kind: kind, ExternalID: target, OwnerUID: owner, TopologyUID: owner, ResourceUID: "target", Generation: 1, InventoryDigest: "inventory"}
					digest, err := candidate.Digest()
					require.NoError(t, err)
					grant, err := controlplane.SignExecutionGrant(key, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: owner, ResourceUID: "target", Generation: 1}, AgentID: "host", SessionID: "session", CommandID: "gc", ActionID: "gc", Attempt: 1, Epoch: 1, Operation: "gc_delete", Digest: digest, TargetBootID: "boot", State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}, "boot", time.Now())
					require.NoError(t, err)
					input := gcCrashInput{Kind: kind, Phase: phase, Root: private(), ProviderRoot: providerRoot, Kernel: kernel, Candidate: candidate, Grant: grant, Issuer: pub, HostKey: hostKey, Ready: filepath.Join(private(), "ready")}
					raw, err := json.Marshal(input)
					require.NoError(t, err)
					path := filepath.Join(private(), "input.json")
					require.NoError(t, os.WriteFile(path, raw, 0600))
					child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGCProviderCrashProcess$")
					child.Env = append(os.Environ(), "SYSBOX_GC_CRASH_INPUT="+path)
					log, err := os.Create(filepath.Join(private(), "child.log"))
					require.NoError(t, err)
					defer log.Close()
					child.Stdout, child.Stderr = log, log
					require.NoError(t, child.Start())
					defer child.Process.Kill()
					require.Eventually(t, func() bool { _, err := os.Stat(input.Ready); return err == nil }, 30*time.Second, 20*time.Millisecond, "child log: %s", log.Name())
					require.NoError(t, child.Process.Kill())
					var exited *exec.ExitError
					require.ErrorAs(t, child.Wait(), &exited)
					require.Equal(t, -1, exited.ExitCode(), "requires OS SIGKILL")
					cold := crashCoordinator(input)
					reports := 0
					cold.ReportStop = func(_ context.Context, receipt controlplane.SignedExecutionStopReceipt) error {
						require.NoError(t, controlplane.VerifyGCDeletionReceipt(hostKey.Public().(ed25519.PublicKey), receipt, candidate))
						reports++
						return nil
					}
					recoverErr := cold.Recover(ctx)
					if phase == "corrupt-journal" {
						require.Error(t, recoverErr)
					} else {
						require.NoError(t, recoverErr)
					}
					_, err = cold.Delete(ctx, candidate, grant, func(context.Context) (json.RawMessage, error) {
						t.Fatal("provider must never replay after crash")
						return nil, nil
					})
					if phase == "before-report" {
						require.ErrorIs(t, err, effectgate.ErrFenced)
						result, observeErr := cold.Observe(ctx, candidate, grant)
						require.NoError(t, observeErr)
						require.JSONEq(t, `{"deleted":true}`, string(result))
						require.GreaterOrEqual(t, reports, 1)
					} else {
						require.ErrorIs(t, err, effectgate.ErrUncertain)
						require.Zero(t, reports)
					}
					require.Equal(t, phase == "before-effect", present(target))
					require.True(t, present(survivor))
					if phase != "before-effect" && stopped != nil {
						stopped(target)
					}
				})
			}
		})
	}
}
