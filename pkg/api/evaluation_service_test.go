package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

type evaluationReadSourceStub struct {
	mu             sync.Mutex
	agents         []controlplane.Agent
	inventories    map[string]controlplane.AgentInventory
	inventoryReads int
	list           func(context.Context) ([]controlplane.Agent, error)
	guestExecAt    time.Time
	guestFileAt    time.Time
}

func (s *evaluationReadSourceStub) LatestGuestAcceptance(context.Context, string) (time.Time, time.Time, error) {
	return s.guestExecAt, s.guestFileAt, nil
}

func (s *evaluationReadSourceStub) ListAgents(ctx context.Context) ([]controlplane.Agent, error) {
	if s.list != nil {
		return s.list(ctx)
	}
	return append([]controlplane.Agent(nil), s.agents...), nil
}

func (s *evaluationReadSourceStub) GetAgentInventory(_ context.Context, agentID string) (*controlplane.AgentInventory, error) {
	s.mu.Lock()
	s.inventoryReads++
	s.mu.Unlock()
	inventory, ok := s.inventories[agentID]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return &inventory, nil
}

type evaluationTestDriver struct{ substrate.BaseSubstrate }

func (evaluationTestDriver) Capabilities() substrate.Capabilities { return substrate.Capabilities{} }
func (evaluationTestDriver) CreateNode(context.Context, substrate.NodeSpec) (substrate.NodeHandle, error) {
	return substrate.NodeHandle{}, nil
}
func (evaluationTestDriver) StartNode(context.Context, substrate.NodeHandle) error   { return nil }
func (evaluationTestDriver) StopNode(context.Context, substrate.NodeHandle) error    { return nil }
func (evaluationTestDriver) DestroyNode(context.Context, substrate.NodeHandle) error { return nil }
func (evaluationTestDriver) NodeStatus(context.Context, substrate.NodeHandle) (bool, error) {
	return true, nil
}
func (evaluationTestDriver) Attach(context.Context, substrate.NodeHandle, driver.AttachmentRequest) (driver.AttachmentResult, error) {
	return driver.AttachmentResult{}, nil
}
func (evaluationTestDriver) Observe(context.Context, substrate.NodeHandle, driver.AttachmentRequest, json.RawMessage) (driver.AttachmentResult, error) {
	return driver.AttachmentResult{}, nil
}
func (evaluationTestDriver) Delete(context.Context, substrate.NodeHandle, driver.AttachmentRequest, json.RawMessage) error {
	return nil
}
func (evaluationTestDriver) ResolveImage(context.Context, substrate.ArtifactSource) (substrate.ArtifactHandle, error) {
	return substrate.ArtifactHandle{}, nil
}
func (evaluationTestDriver) CreateIsolated(context.Context, driver.IsolatedNetworkSpec) error {
	return nil
}
func (evaluationTestDriver) DeleteIsolated(context.Context, driver.IsolatedNetworkSpec) error {
	return nil
}
func (evaluationTestDriver) NetworkHealthy(context.Context, driver.IsolatedNetworkSpec) (bool, string) {
	return true, ""
}
func (evaluationTestDriver) LinkHealthy(context.Context, string, string) bool { return true }
func (evaluationTestDriver) DeleteAttachment(context.Context, string, string, string) error {
	return nil
}

func registerEvaluationTestDriver(t *testing.T) {
	t.Helper()
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	d := evaluationTestDriver{}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{
		Name: "test", Version: "test", Node: d, NIC: d, NodeState: d, Artifact: d,
	}))
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{
		Name: "network", Version: "test", LinuxNetwork: d,
	}))
}

func evaluationRequest(hcl string) EvaluationRequest {
	sum := sha256.Sum256([]byte(hcl))
	return EvaluationRequest{SchemaVersion: EvaluationSchemaVersion, HCL: hcl, ConfigSHA256: hex.EncodeToString(sum[:])}
}

func TestDecodeEvaluationRequestIsStrictAndBounded(t *testing.T) {
	valid := `{"schema_version":1,"hcl":"","config_sha256":"` + strings.Repeat("0", 64) + `"}`
	request, err := DecodeEvaluationRequest(strings.NewReader(valid))
	require.NoError(t, err)
	require.Equal(t, EvaluationSchemaVersion, request.SchemaVersion)

	_, err = DecodeEvaluationRequest(strings.NewReader(strings.TrimSuffix(valid, "}") + `,"unknown":true}`))
	require.Error(t, err)

	_, err = DecodeEvaluationRequest(strings.NewReader(valid + valid))
	require.Error(t, err)

	oversized := `{"schema_version":1,"hcl":"` + strings.Repeat("x", EvaluationMaxHCLBytes+1) + `","config_sha256":"` + strings.Repeat("0", 64) + `"}`
	_, err = DecodeEvaluationRequest(strings.NewReader(oversized))
	require.Error(t, err)
}

func TestEvaluationRejectsHashMismatch(t *testing.T) {
	request := evaluationRequest(`resource "sysbox_network" "lab" { cidr = "10.0.0.0/24" }`)
	request.ConfigSHA256 = strings.Repeat("0", 64)

	_, err := NewEvaluationService().Evaluate(context.Background(), request)
	require.ErrorIs(t, err, ErrInvalidEvaluationRequest)
}

func TestEvaluationRejectsEmptyHCL(t *testing.T) {
	_, err := NewEvaluationService().Evaluate(context.Background(), evaluationRequest(""))
	require.ErrorIs(t, err, ErrInvalidEvaluationRequest)
}

func TestEvaluationRestrictedProfileIsRedacted(t *testing.T) {
	tests := map[string]string{
		"module": `module "outside" { source = "/private/module.hcl" }`,
		"data": `data "sysbox_node" "existing" {
  substrate = "test"
  id = "private-node"
}`,
		"env":          `locals { token = env("PRIVATE_TOKEN") }`,
		"env_optional": `locals { token = env_optional("PRIVATE_TOKEN") }`,
		"secret":       `locals { token = "secret://env/PRIVATE_TOKEN" }`,
		"count": `resource "sysbox_network" "lab" {
  count = 3
  cidr = "10.0.0.0/24"
}`,
		"for_each": `resource "sysbox_network" "lab" {
  for_each = toset(["private"])
  cidr = "10.0.0.0/24"
}`,
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			response, err := NewEvaluationService().Evaluate(context.Background(), evaluationRequest(source))
			require.NoError(t, err)
			require.Equal(t, EvaluationDecisionInvalid, response.Decision)
			require.Contains(t, response.Checks, EvaluationCheck{
				Name: EvaluationCheckConfigProfile, State: EvaluationCheckFailed,
				Class: EvaluationClassValidation, Action: EvaluationActionFixConfig,
			})
			encoded, marshalErr := json.Marshal(response)
			require.NoError(t, marshalErr)
			require.NotContains(t, string(encoded), "PRIVATE")
			require.NotContains(t, string(encoded), "/private")
		})
	}
}

func TestEvaluationProfileAllowsOnlyInfrastructureArtifactEnvironment(t *testing.T) {
	t.Setenv("SYSBOX_ROOTFS", "/var/lib/sysbox/rootfs.ext4")
	t.Setenv("SYSBOX_QCOW2", "/var/lib/sysbox/image.qcow2")
	t.Setenv("SYSBOX_CACHE", "/var/cache/sysbox")
	t.Setenv("HOME", "/var/lib/sysbox")

	allowed := `locals {
  rootfs = env_optional("SYSBOX_ROOTFS") != "" ? env_optional("SYSBOX_ROOTFS") : "${env_optional("SYSBOX_CACHE")}/rootfs.ext4"
  qcow2 = env("SYSBOX_QCOW2")
  home = env_optional("HOME")
}`
	response, err := NewEvaluationService().Evaluate(context.Background(), evaluationRequest(allowed))
	require.NoError(t, err)
	require.NotContains(t, response.Checks, failedCheck(EvaluationCheckConfigProfile, EvaluationClassValidation))
	require.Contains(t, response.Checks, passedCheck(EvaluationCheckConfigProfile, EvaluationClassValidation))

	for name, source := range map[string]string{
		"unlisted": `locals { value = env_optional("PRIVATE_TOKEN") }`,
		"dynamic": `locals {
  name = "SYSBOX_ROOTFS"
  value = env_optional(local.name)
}`,
	} {
		t.Run(name, func(t *testing.T) {
			response, err := NewEvaluationService().Evaluate(context.Background(), evaluationRequest(source))
			require.NoError(t, err)
			require.Contains(t, response.Checks, failedCheck(EvaluationCheckConfigProfile, EvaluationClassValidation))
		})
	}
}

func TestEvaluationParseFailureIsRedacted(t *testing.T) {
	response, err := NewEvaluationService().Evaluate(context.Background(), evaluationRequest(`resource "secret-name"`))
	require.NoError(t, err)
	require.Equal(t, EvaluationDecisionInvalid, response.Decision)
	require.Equal(t, []EvaluationCheck{{
		Name: EvaluationCheckConfigSyntax, State: EvaluationCheckFailed,
		Class: EvaluationClassValidation, Action: EvaluationActionFixConfig,
	}}, response.Checks)
	encoded, marshalErr := json.Marshal(response)
	require.NoError(t, marshalErr)
	require.NotContains(t, string(encoded), "secret-name")
}

func TestEvaluationComputesBoundedThreeAndTenNodeEnvelopes(t *testing.T) {
	registerEvaluationTestDriver(t)
	for _, nodes := range []int{3, 10} {
		t.Run(fmt.Sprintf("%d_nodes", nodes), func(t *testing.T) {
			source := evaluationTopology(nodes)
			response, err := NewEvaluationService().Evaluate(context.Background(), evaluationRequest(source))
			require.NoError(t, err)
			require.Equalf(t, EvaluationDecisionIndeterminate, response.Decision, "checks: %#v", response.Checks)
			require.True(t, response.Plan.Valid)
			require.False(t, response.Plan.Applicable)
			require.Equal(t, []EvaluationPlanAction{
				{Action: "create", ResourceType: "sysbox_image", Count: 1},
				{Action: "create", ResourceType: "sysbox_network", Count: 1},
				{Action: "create", ResourceType: "sysbox_node", Count: nodes},
			}, response.Plan.Actions)
			require.Equal(t, EvaluationEnvelope{
				Complete: true,
				Nodes:    []EvaluationNodeCount{{Substrate: "test", Count: nodes}},
				VCPU:     nodes * 2, MemoryBytes: int64(nodes) * 512 * 1024 * 1024,
				NetworkCount: 1, AttachmentCount: nodes, ArtifactCount: 0,
			}, response.Envelope)
			require.LessOrEqual(t, len(response.Checks), EvaluationMaxChecks)
		})
	}
}

func TestEvaluationIncompleteSizingPreservesNodeAndAttachmentCounts(t *testing.T) {
	registerEvaluationTestDriver(t)
	source := strings.Replace(evaluationTopology(1), "  vcpus = 2\n  memory = \"512\"\n", "", 1)

	response, err := NewEvaluationService().Evaluate(context.Background(), evaluationRequest(source))

	require.NoError(t, err)
	require.Equal(t, EvaluationDecisionIndeterminate, response.Decision)
	require.Equal(t, []EvaluationNodeCount{{Substrate: "test", Count: 1}}, response.Envelope.Nodes)
	require.Equal(t, 1, response.Envelope.AttachmentCount)
	require.False(t, response.Envelope.Complete)
}

func TestEvaluationRejectsNodeLimit(t *testing.T) {
	registerEvaluationTestDriver(t)

	response, err := NewEvaluationService().Evaluate(context.Background(), evaluationRequest(evaluationTopology(EvaluationMaxNodes+1)))

	require.NoError(t, err)
	require.Equal(t, EvaluationDecisionInvalid, response.Decision)
	require.Contains(t, response.Checks, EvaluationCheck{
		Name: EvaluationCheckEnvelope, State: EvaluationCheckFailed,
		Class: EvaluationClassPlanning, Action: EvaluationActionFixConfig,
	})
}

func TestEvaluationWithoutReadSourceIsIndeterminate(t *testing.T) {
	registerEvaluationTestDriver(t)
	response, err := NewEvaluationService().Evaluate(context.Background(), evaluationRequest(evaluationTopology(3)))
	require.NoError(t, err)
	require.Equal(t, EvaluationDecisionIndeterminate, response.Decision)
	require.Contains(t, response.Checks, unknownCheck(EvaluationCheckAgentSelection, EvaluationClassCapacity, EvaluationActionSelectAgent))
}

func TestEvaluationBoundsAgentScanAndUsesOneClockSnapshot(t *testing.T) {
	registerEvaluationTestDriver(t)
	now := time.Now().UTC()
	agents := make([]controlplane.Agent, EvaluationMaxAgents+1)
	for i := range agents {
		agents[i] = controlplane.Agent{ID: fmt.Sprintf("agent-%03d", i), Status: controlplane.AgentStatusOnline, Protocol: controlplane.AgentProtocolVersion, LastHeartbeat: now, Capabilities: []string{"network", "test"}}
	}
	source := &evaluationReadSourceStub{agents: agents, inventories: map[string]controlplane.AgentInventory{}}
	service := NewEvaluationService(source)
	clockReads := 0
	service.now = func() time.Time {
		clockReads++
		return now
	}
	response, err := service.Evaluate(context.Background(), evaluationRequest(evaluationTopology(3)))
	require.NoError(t, err)
	require.Equal(t, EvaluationDecisionIndeterminate, response.Decision)
	require.Equal(t, 0, source.inventoryReads)
	require.Equal(t, 1, clockReads)
}

func TestEvaluationRequiresFreshStatusAndHeartbeat(t *testing.T) {
	registerEvaluationTestDriver(t)
	now := time.Now().UTC()
	source := &evaluationReadSourceStub{
		agents: []controlplane.Agent{{ID: "host-a", Status: controlplane.AgentStatusOnline, Protocol: controlplane.AgentProtocolVersion, Capabilities: []string{"network", "test"}}},
		inventories: map[string]controlplane.AgentInventory{
			"host-a": {AgentID: "host-a", Status: "unknown", Capabilities: []string{"network", "test"}, ObservedAt: now},
		},
	}
	response, err := NewEvaluationService(source).Evaluate(context.Background(), evaluationRequest(evaluationTopology(3)))
	require.NoError(t, err)
	require.Nil(t, response.SelectedAgent)
	require.Equal(t, EvaluationDecisionIndeterminate, response.Decision)
}

func TestEvaluationSelectsAgentDeterministicallyAndFailsClosedOnUnknownEvidence(t *testing.T) {
	registerEvaluationTestDriver(t)
	now := time.Now().UTC()
	capabilities := []string{"network", "test"}
	source := evaluationReadSourceStub{
		agents: []controlplane.Agent{
			{ID: "host-z", Status: controlplane.AgentStatusOnline, Protocol: controlplane.AgentProtocolVersion, Capabilities: capabilities, LastHeartbeat: now},
			{ID: "host-a", Status: controlplane.AgentStatusOnline, Protocol: controlplane.AgentProtocolVersion, Capabilities: capabilities, LastHeartbeat: now},
		},
		inventories: map[string]controlplane.AgentInventory{
			"host-a": {AgentID: "host-a", Capabilities: capabilities, Status: "fresh", ObservedAt: now},
			"host-z": {AgentID: "host-z", Capabilities: capabilities, Status: "fresh", ObservedAt: now},
		},
	}

	response, err := NewEvaluationService(&source).Evaluate(context.Background(), evaluationRequest(evaluationTopology(3)))

	require.NoError(t, err)
	require.Equal(t, &EvaluationSelectedAgent{ID: "host-a", Protocol: controlplane.AgentProtocolVersion}, response.SelectedAgent)
	require.Equal(t, EvaluationDecisionIndeterminate, response.Decision)
	require.Contains(t, response.Checks, passedCheck(EvaluationCheckAgentSelection, EvaluationClassCapacity))
	require.Len(t, response.Checks, 11)
	for _, name := range []EvaluationCheckName{EvaluationCheckCapacityCPU, EvaluationCheckCapacityMemory, EvaluationCheckCapacityDisk, EvaluationCheckAcceptanceEvidence} {
		require.Contains(t, response.Checks, unknownCheck(name, EvaluationClassCapacity, EvaluationActionCollectEvidence))
	}
	require.Contains(t, response.Checks, passedCheck(EvaluationCheckArtifactEvidence, EvaluationClassCapacity))
}

func TestEvaluationIsReadyWithCapacityArtifactsAndFreshAcceptanceEvidence(t *testing.T) {
	registerEvaluationTestDriver(t)
	now := time.Now().UTC()
	capabilities := []string{"network", "test"}
	source := evaluationReadSourceStub{
		agents: []controlplane.Agent{{ID: "host-a", Status: controlplane.AgentStatusOnline, Protocol: controlplane.AgentProtocolVersion, Capabilities: capabilities, LastHeartbeat: now}},
		inventories: map[string]controlplane.AgentInventory{
			"host-a": {
				AgentID: "host-a", Capabilities: capabilities, Status: "fresh", ObservedAt: now,
				Resources: controlplane.HostResourceInventory{CPU: 64, MemoryBytes: 64 << 30, DiskBytes: 1 << 40, ArtifactBytes: 1 << 30},
				Artifacts: []controlplane.InventoryItem{{Kind: "kernel", Available: true}},
			},
		},
		guestExecAt: now.Add(-time.Minute), guestFileAt: now.Add(-time.Minute),
	}
	service := NewEvaluationService(&source)
	service.now = func() time.Time { return now }

	response, err := service.Evaluate(context.Background(), evaluationRequest(evaluationTopology(3)))

	require.NoError(t, err)
	require.Equal(t, EvaluationDecisionReady, response.Decision)
	require.True(t, response.Plan.Applicable)
	require.Equal(t, source.inventories["host-a"].Resources, response.Evidence.Resources)
	require.Equal(t, capabilities, response.Evidence.Capabilities)
	require.Equal(t, now.Add(-time.Minute), response.Evidence.GuestExecAt)
	require.Equal(t, now.Add(-time.Minute), response.Evidence.GuestFileAt)
	for _, name := range []EvaluationCheckName{EvaluationCheckCapacityCPU, EvaluationCheckCapacityMemory, EvaluationCheckCapacityDisk, EvaluationCheckArtifactEvidence, EvaluationCheckAcceptanceEvidence} {
		require.Contains(t, response.Checks, passedCheck(name, EvaluationClassCapacity))
	}
}

func TestEvaluationCPUOvercommitIsExplicitAndAudited(t *testing.T) {
	registerEvaluationTestDriver(t)
	now := time.Now().UTC()
	capabilities := []string{"network", "test"}
	source := evaluationReadSourceStub{
		agents: []controlplane.Agent{{ID: "host-a", Status: controlplane.AgentStatusOnline, Protocol: controlplane.AgentProtocolVersion, Capabilities: capabilities, LastHeartbeat: now}},
		inventories: map[string]controlplane.AgentInventory{
			"host-a": {
				AgentID: "host-a", Capabilities: capabilities, Status: "fresh", ObservedAt: now,
				Resources: controlplane.HostResourceInventory{CPU: 4, MemoryBytes: 64 << 30, DiskBytes: 1 << 40, ArtifactBytes: 1 << 30},
			},
		},
		guestExecAt: now.Add(-time.Minute), guestFileAt: now.Add(-time.Minute),
	}
	service := NewEvaluationServiceWithPolicy(&source, EvaluationPolicy{CPUOvercommitRatio: 2})
	service.now = func() time.Time { return now }

	response, err := service.Evaluate(context.Background(), evaluationRequest(evaluationTopology(3)))

	require.NoError(t, err)
	require.Equal(t, EvaluationDecisionReady, response.Decision)
	require.Equal(t, int64(4), response.Evidence.Resources.CPU)
	require.Equal(t, int64(2), response.Evidence.CPUOvercommitRatio)
	require.Equal(t, int64(8), response.Evidence.EffectiveCPU)
	require.Contains(t, response.Checks, passedCheck(EvaluationCheckCapacityCPU, EvaluationClassCapacity))
}

func TestEvaluationCountsOnlyArtifactsThatMustBeLocal(t *testing.T) {
	registerEvaluationTestDriver(t)
	source := evaluationTopology(1) + `
resource "sysbox_image" "rootfs" {
  substrate = "test"
  kind = "rootfs"
  source = "/var/lib/sysbox/rootfs.ext4"
  architecture = "amd64"
  guest_family = "linux"
}
resource "sysbox_kernel" "linux" {
  substrate = "test"
  source = "https://example.invalid/vmlinux"
  architecture = "amd64"
}
`

	response, err := NewEvaluationService().Evaluate(context.Background(), evaluationRequest(source))

	require.NoError(t, err)
	require.Equal(t, 2, response.Envelope.ArtifactCount)
}

func TestEvaluationPreferredAgentIsStrict(t *testing.T) {
	registerEvaluationTestDriver(t)
	now := time.Now().UTC()
	capabilities := []string{"network", "test"}
	source := evaluationReadSourceStub{
		agents: []controlplane.Agent{
			{ID: "preferred", Status: controlplane.AgentStatusOffline, Protocol: controlplane.AgentProtocolVersion, Capabilities: capabilities, LastHeartbeat: now},
			{ID: "fallback", Status: controlplane.AgentStatusOnline, Protocol: controlplane.AgentProtocolVersion, Capabilities: capabilities, LastHeartbeat: now},
		},
		inventories: map[string]controlplane.AgentInventory{
			"preferred": {AgentID: "preferred", Capabilities: capabilities, Status: "fresh", ObservedAt: now},
			"fallback":  {AgentID: "fallback", Capabilities: capabilities, Status: "fresh", ObservedAt: now},
		},
	}
	request := evaluationRequest(evaluationTopology(3))
	request.PreferredAgentID = "preferred"

	response, err := NewEvaluationService(&source).Evaluate(context.Background(), request)

	require.NoError(t, err)
	require.Nil(t, response.SelectedAgent)
	require.Equal(t, EvaluationDecisionIndeterminate, response.Decision)
	require.Contains(t, response.Checks, unknownCheck(EvaluationCheckAgentSelection, EvaluationClassCapacity, EvaluationActionSelectAgent))
}

func TestEvaluationRejectsIncompleteAgentCapabilitiesAndStaleInventory(t *testing.T) {
	registerEvaluationTestDriver(t)
	source := evaluationReadSourceStub{
		agents: []controlplane.Agent{
			{ID: "missing-capability", Status: controlplane.AgentStatusOnline, Protocol: controlplane.AgentProtocolVersion, Capabilities: []string{"node"}, LastHeartbeat: time.Now().UTC()},
			{ID: "stale", Status: controlplane.AgentStatusOnline, Protocol: controlplane.AgentProtocolVersion, Capabilities: []string{"network", "test"}, LastHeartbeat: time.Now().UTC()},
		},
		inventories: map[string]controlplane.AgentInventory{
			"missing-capability": {AgentID: "missing-capability", Capabilities: []string{"node"}, Status: "fresh", ObservedAt: time.Now().UTC()},
			"stale":              {AgentID: "stale", Capabilities: []string{"network", "test"}, Status: "fresh", ObservedAt: time.Now().UTC().Add(-3 * time.Minute)},
		},
	}

	response, err := NewEvaluationService(&source).Evaluate(context.Background(), evaluationRequest(evaluationTopology(3)))

	require.NoError(t, err)
	require.Nil(t, response.SelectedAgent)
	require.Equal(t, EvaluationDecisionIndeterminate, response.Decision)
}

func TestEvaluationConcurrentResponsesAreDeterministic(t *testing.T) {
	registerEvaluationTestDriver(t)
	capabilities := []string{"network", "test"}
	source := evaluationReadSourceStub{
		agents: []controlplane.Agent{
			{ID: "host-b", Status: controlplane.AgentStatusOnline, Protocol: controlplane.AgentProtocolVersion, Capabilities: capabilities, LastHeartbeat: time.Now().UTC()},
			{ID: "host-a", Status: controlplane.AgentStatusOnline, Protocol: controlplane.AgentProtocolVersion, Capabilities: capabilities, LastHeartbeat: time.Now().UTC()},
		},
		inventories: map[string]controlplane.AgentInventory{
			"host-a": {AgentID: "host-a", Capabilities: capabilities, Status: "fresh", ObservedAt: time.Now().UTC()},
			"host-b": {AgentID: "host-b", Capabilities: capabilities, Status: "fresh", ObservedAt: time.Now().UTC()},
		},
	}
	service := NewEvaluationService(&source)
	request := evaluationRequest(evaluationTopology(10))
	const workers = 32
	type evaluationResult struct {
		encoded string
		err     error
	}
	results := make(chan evaluationResult, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			response, err := service.Evaluate(context.Background(), request)
			if err != nil {
				results <- evaluationResult{err: err}
				return
			}
			encoded, err := json.Marshal(response)
			results <- evaluationResult{encoded: string(encoded), err: err}
		}()
	}
	group.Wait()
	close(results)
	var baseline string
	for result := range results {
		require.NoError(t, result.err)
		if baseline == "" {
			baseline = result.encoded
		}
		require.Equal(t, baseline, result.encoded)
	}
}

func TestEvaluationHandlerIsStrictAndHasNoDurableWrites(t *testing.T) {
	registerEvaluationTestDriver(t)
	runsDir := t.TempDir()
	workspacesDir := t.TempDir()
	server := NewServer(runsDir, workspacesDir)
	capabilities := []string{"network", "test"}
	require.NoError(t, server.apiStore.SaveAgent(context.Background(), controlplane.Agent{
		ID: "host-a", Status: controlplane.AgentStatusOnline,
		Protocol: controlplane.AgentProtocolVersion, Capabilities: capabilities, LastHeartbeat: time.Now().UTC(),
		AuthSecret: "PRIVATE_AGENT_SECRET",
	}))
	require.NoError(t, server.apiStore.SaveAgentInventory(context.Background(), controlplane.AgentInventory{
		AgentID: "host-a", Capabilities: capabilities, Status: "fresh", ObservedAt: time.Now().UTC(),
		Artifacts: []controlplane.InventoryItem{{Path: "/private/artifact", Available: true}},
	}))
	before := evaluationFileSnapshot(t, runsDir, workspacesDir)
	request := evaluationRequest(evaluationTopology(3))
	body, err := json.Marshal(request)
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/evaluations", bytes.NewReader(body)))

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response EvaluationResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, EvaluationDecisionIndeterminate, response.Decision)
	require.Equal(t, &EvaluationSelectedAgent{ID: "host-a", Protocol: controlplane.AgentProtocolVersion}, response.SelectedAgent)
	require.NotContains(t, recorder.Body.String(), "PRIVATE_AGENT_SECRET")
	require.NotContains(t, recorder.Body.String(), "/private/artifact")
	require.Equal(t, before, evaluationFileSnapshot(t, runsDir, workspacesDir))

	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/evaluations", strings.NewReader(`{"schema_version":1,"unknown":true}`)))
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "unknown")
}

func evaluationFileSnapshot(t *testing.T, roots ...string) []string {
	t.Helper()
	var result []string
	for _, root := range roots {
		require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			digest := ""
			if info.Mode().IsRegular() {
				content, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				sum := sha256.Sum256(content)
				digest = hex.EncodeToString(sum[:])
			}
			result = append(result, fmt.Sprintf("%s:%s:%d:%d:%d:%s", root, relative, info.Mode(), info.Size(), info.ModTime().UnixNano(), digest))
			return nil
		}))
	}
	sort.Strings(result)
	return result
}

func evaluationTopology(nodes int) string {
	var source bytes.Buffer
	source.WriteString(`
resource "sysbox_image" "base" {
  substrate = "test"
  kind = "oci"
  source = "example.invalid/base:fixed"
  architecture = "amd64"
  guest_family = "linux"
}
resource "sysbox_network" "lab" { cidr = "10.0.0.0/24" }
`)
	for i := 0; i < nodes; i++ {
		fmt.Fprintf(&source, `
resource "sysbox_node" "node_%d" {
  substrate = "test"
  image = sysbox_image.base.id
  vcpus = 2
  memory = "512"
  link "lab" {
    network = sysbox_network.lab.id
    ip = "10.0.0.%d/24"
  }
}
`, i, i+10)
	}
	return source.String()
}
