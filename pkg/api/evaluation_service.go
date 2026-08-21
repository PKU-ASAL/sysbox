package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/secret"
	"github.com/oslab/sysbox/pkg/state"
)

const (
	EvaluationSchemaVersion  = 1
	EvaluationMaxWireBytes   = 2 << 20
	EvaluationMaxHCLBytes    = 1 << 20
	EvaluationMaxResources   = 256
	EvaluationMaxNodes       = 128
	EvaluationMaxNetworks    = 128
	EvaluationMaxArtifacts   = 128
	EvaluationMaxAttachments = 1024
	EvaluationMaxChecks      = 32
	EvaluationMaxAgents      = 128
)

var evaluationInfrastructureEnvironment = map[string]struct{}{
	"HOME":          {},
	"SYSBOX_CACHE":  {},
	"SYSBOX_QCOW2":  {},
	"SYSBOX_ROOTFS": {},
}

var ErrInvalidEvaluationRequest = errors.New("invalid evaluation request")

type EvaluationDecision string

const (
	EvaluationDecisionReady         EvaluationDecision = "ready"
	EvaluationDecisionInvalid       EvaluationDecision = "invalid"
	EvaluationDecisionIndeterminate EvaluationDecision = "indeterminate"
)

type EvaluationCheckState string
type EvaluationCheckClass string
type EvaluationCheckAction string
type EvaluationCheckName string

const (
	EvaluationCheckPassed  EvaluationCheckState = "passed"
	EvaluationCheckFailed  EvaluationCheckState = "failed"
	EvaluationCheckUnknown EvaluationCheckState = "unknown"

	EvaluationClassValidation EvaluationCheckClass = "validation"
	EvaluationClassPlanning   EvaluationCheckClass = "planning"
	EvaluationClassCapacity   EvaluationCheckClass = "capacity"

	EvaluationActionNone             EvaluationCheckAction = "none"
	EvaluationActionFixConfig        EvaluationCheckAction = "fix_config"
	EvaluationActionDeclareResources EvaluationCheckAction = "declare_resources"
	EvaluationActionSelectAgent      EvaluationCheckAction = "select_agent"
	EvaluationActionCollectEvidence  EvaluationCheckAction = "collect_evidence"

	EvaluationCheckConfigSyntax       EvaluationCheckName = "config.syntax"
	EvaluationCheckConfigProfile      EvaluationCheckName = "config.profile"
	EvaluationCheckGraphBuild         EvaluationCheckName = "graph.build"
	EvaluationCheckPlanCompute        EvaluationCheckName = "plan.compute"
	EvaluationCheckEnvelope           EvaluationCheckName = "envelope.complete"
	EvaluationCheckAgentSelection     EvaluationCheckName = "agent.selection"
	EvaluationCheckCapacityCPU        EvaluationCheckName = "capacity.cpu"
	EvaluationCheckCapacityMemory     EvaluationCheckName = "capacity.memory"
	EvaluationCheckCapacityDisk       EvaluationCheckName = "capacity.disk"
	EvaluationCheckArtifactEvidence   EvaluationCheckName = "artifact.evidence"
	EvaluationCheckAcceptanceEvidence EvaluationCheckName = "acceptance.evidence"
)

type EvaluationRequest struct {
	SchemaVersion    int    `json:"schema_version"`
	HCL              string `json:"hcl"`
	ConfigSHA256     string `json:"config_sha256"`
	PreferredAgentID string `json:"preferred_agent_id,omitempty"`
}

type EvaluationCheck struct {
	Name   EvaluationCheckName   `json:"name"`
	State  EvaluationCheckState  `json:"state"`
	Class  EvaluationCheckClass  `json:"class"`
	Action EvaluationCheckAction `json:"action"`
}

type EvaluationPlanAction struct {
	Action       string `json:"action"`
	ResourceType string `json:"resource_type"`
	Count        int    `json:"count"`
}

type EvaluationPlan struct {
	Valid      bool                   `json:"valid"`
	Applicable bool                   `json:"applicable"`
	Actions    []EvaluationPlanAction `json:"actions,omitempty"`
}

type EvaluationNodeCount struct {
	Substrate string `json:"substrate"`
	Count     int    `json:"count"`
}

type EvaluationEnvelope struct {
	Complete        bool                  `json:"complete"`
	Nodes           []EvaluationNodeCount `json:"nodes,omitempty"`
	VCPU            int                   `json:"vcpu"`
	MemoryBytes     int64                 `json:"memory_bytes"`
	NetworkCount    int                   `json:"network_count"`
	AttachmentCount int                   `json:"attachment_count"`
	ArtifactCount   int                   `json:"artifact_count"`
}

type EvaluationSelectedAgent struct {
	ID       string `json:"id"`
	Protocol string `json:"protocol"`
}

type EvaluationEvidence struct {
	Resources          controlplane.HostResourceInventory `json:"resources"`
	Capabilities       []string                           `json:"capabilities,omitempty"`
	AvailableArtifacts int                                `json:"available_artifacts"`
	CPUOvercommitRatio int64                              `json:"cpu_overcommit_ratio"`
	EffectiveCPU       int64                              `json:"effective_cpu"`
	GuestExecAt        time.Time                          `json:"guest_exec_at,omitempty"`
	GuestFileAt        time.Time                          `json:"guest_file_at,omitempty"`
}

type EvaluationResponse struct {
	SchemaVersion int                      `json:"schema_version"`
	ConfigSHA256  string                   `json:"config_sha256"`
	Decision      EvaluationDecision       `json:"decision"`
	Plan          EvaluationPlan           `json:"plan"`
	Envelope      EvaluationEnvelope       `json:"envelope"`
	SelectedAgent *EvaluationSelectedAgent `json:"selected_agent,omitempty"`
	Evidence      EvaluationEvidence       `json:"evidence"`
	Checks        []EvaluationCheck        `json:"checks"`
}

type EvaluationReadSource interface {
	ListAgents(context.Context) ([]controlplane.Agent, error)
	GetAgentInventory(context.Context, string) (*controlplane.AgentInventory, error)
	LatestGuestAcceptance(context.Context, string) (time.Time, time.Time, error)
}

type EvaluationService struct {
	source             EvaluationReadSource
	now                func() time.Time
	cpuOvercommitRatio int64
}

type EvaluationPolicy struct {
	CPUOvercommitRatio int64
}

func NewEvaluationService(sources ...EvaluationReadSource) *EvaluationService {
	service := &EvaluationService{now: time.Now, cpuOvercommitRatio: 1}
	if len(sources) > 0 {
		service.source = sources[0]
	}
	return service
}

func NewEvaluationServiceWithPolicy(source EvaluationReadSource, policy EvaluationPolicy) *EvaluationService {
	service := NewEvaluationService(source)
	if policy.CPUOvercommitRatio >= 1 && policy.CPUOvercommitRatio <= 16 {
		service.cpuOvercommitRatio = policy.CPUOvercommitRatio
	}
	return service
}

func DecodeEvaluationRequest(reader io.Reader) (EvaluationRequest, error) {
	limited := io.LimitReader(reader, EvaluationMaxWireBytes+1)
	payload, err := io.ReadAll(limited)
	if err != nil || len(payload) > EvaluationMaxWireBytes {
		return EvaluationRequest{}, ErrInvalidEvaluationRequest
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var request EvaluationRequest
	if err := decoder.Decode(&request); err != nil {
		return EvaluationRequest{}, ErrInvalidEvaluationRequest
	}
	if err := requireJSONEOF(decoder); err != nil {
		return EvaluationRequest{}, ErrInvalidEvaluationRequest
	}
	if len(request.HCL) > EvaluationMaxHCLBytes {
		return EvaluationRequest{}, ErrInvalidEvaluationRequest
	}
	return request, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrInvalidEvaluationRequest
	}
	return nil
}

func (s *EvaluationService) Evaluate(ctx context.Context, request EvaluationRequest) (EvaluationResponse, error) {
	if err := validateEvaluationRequest(request); err != nil {
		return EvaluationResponse{}, err
	}
	now := time.Now().UTC()
	if s != nil && s.now != nil {
		now = s.now().UTC()
	}
	response := EvaluationResponse{
		SchemaVersion: EvaluationSchemaVersion,
		ConfigSHA256:  request.ConfigSHA256,
		Decision:      EvaluationDecisionInvalid,
		Plan:          EvaluationPlan{Applicable: false},
	}
	root, err := config.ParseString(request.HCL, "evaluation.hcl")
	if err != nil {
		response.Checks = []EvaluationCheck{failedCheck(EvaluationCheckConfigSyntax, EvaluationClassValidation)}
		return response, nil
	}
	response.Checks = append(response.Checks, passedCheck(EvaluationCheckConfigSyntax, EvaluationClassValidation))
	if !evaluationProfileAllowed(request.HCL, root) {
		response.Checks = append(response.Checks, failedCheck(EvaluationCheckConfigProfile, EvaluationClassValidation))
		return response, nil
	}
	response.Checks = append(response.Checks, passedCheck(EvaluationCheckConfigProfile, EvaluationClassValidation))

	evalContext, err := config.BuildEvalContext(root)
	if err != nil {
		response.Checks = append(response.Checks, failedCheck(EvaluationCheckGraphBuild, EvaluationClassValidation))
		return response, nil
	}
	topology, err := runtime.BuildGraph(root, evalContext)
	if err != nil || len(topology.All()) > EvaluationMaxResources {
		response.Checks = append(response.Checks, failedCheck(EvaluationCheckGraphBuild, EvaluationClassValidation))
		return response, nil
	}
	response.Checks = append(response.Checks, passedCheck(EvaluationCheckGraphBuild, EvaluationClassValidation))

	plan, err := runtime.ComputePlan(topology, &state.State{Version: state.SchemaVersion})
	if err != nil {
		response.Checks = append(response.Checks, failedCheck(EvaluationCheckPlanCompute, EvaluationClassPlanning))
		return response, nil
	}
	response.Plan.Valid = true
	response.Plan.Actions = aggregateEvaluationPlan(plan)
	response.Checks = append(response.Checks, passedCheck(EvaluationCheckPlanCompute, EvaluationClassPlanning))

	envelope, complete, withinLimits := buildEvaluationEnvelope(topology)
	response.Envelope = envelope
	if !withinLimits {
		response.Checks = append(response.Checks, failedCheck(EvaluationCheckEnvelope, EvaluationClassPlanning))
		return response, nil
	}
	if !complete {
		response.Decision = EvaluationDecisionIndeterminate
		response.Checks = append(response.Checks, EvaluationCheck{
			Name: EvaluationCheckEnvelope, State: EvaluationCheckUnknown,
			Class: EvaluationClassPlanning, Action: EvaluationActionDeclareResources,
		})
		return response, nil
	}
	response.Checks = append(response.Checks, passedCheck(EvaluationCheckEnvelope, EvaluationClassPlanning))
	if s == nil || s.source == nil {
		response.Decision = EvaluationDecisionIndeterminate
		response.Checks = append(response.Checks, unknownCheck(EvaluationCheckAgentSelection, EvaluationClassCapacity, EvaluationActionSelectAgent))
		return response, nil
	}
	selected, inventory := s.selectAgent(ctx, request.PreferredAgentID, requiredEvaluationCapabilities(topology), now)
	if selected == nil {
		response.Decision = EvaluationDecisionIndeterminate
		response.Checks = append(response.Checks, unknownCheck(
			EvaluationCheckAgentSelection,
			EvaluationClassCapacity,
			EvaluationActionSelectAgent,
		))
		return response, nil
	}
	response.SelectedAgent = &EvaluationSelectedAgent{ID: selected.ID, Protocol: selected.Protocol}
	response.Checks = append(response.Checks, passedCheck(EvaluationCheckAgentSelection, EvaluationClassCapacity))
	capacity := inventory.Resources
	ratio := s.cpuOvercommitRatio
	if ratio < 1 {
		ratio = 1
	}
	effectiveCPU := capacity.CPU * ratio
	checksReady := true
	checksReady = appendEvidenceCheck(&response, EvaluationCheckCapacityCPU, effectiveCPU >= int64(response.Envelope.VCPU)) && checksReady
	checksReady = appendEvidenceCheck(&response, EvaluationCheckCapacityMemory, capacity.MemoryBytes >= response.Envelope.MemoryBytes) && checksReady
	checksReady = appendEvidenceCheck(&response, EvaluationCheckCapacityDisk, capacity.DiskBytes >= response.Envelope.MemoryBytes) && checksReady
	availableArtifacts := 0
	for _, artifact := range inventory.Artifacts {
		if artifact.Available {
			availableArtifacts++
		}
	}
	artifactReady := response.Envelope.ArtifactCount == 0 || (availableArtifacts >= response.Envelope.ArtifactCount && capacity.ArtifactBytes > 0)
	checksReady = appendEvidenceCheck(&response, EvaluationCheckArtifactEvidence, artifactReady) && checksReady
	guestExecAt, guestFileAt, acceptanceErr := s.source.LatestGuestAcceptance(ctx, selected.ID)
	response.Evidence = EvaluationEvidence{Resources: capacity, Capabilities: append([]string(nil), inventory.Capabilities...), AvailableArtifacts: availableArtifacts, CPUOvercommitRatio: ratio, EffectiveCPU: effectiveCPU, GuestExecAt: guestExecAt.UTC(), GuestFileAt: guestFileAt.UTC()}
	sort.Strings(response.Evidence.Capabilities)
	acceptanceReady := acceptanceErr == nil && freshAcceptance(guestExecAt, now) && freshAcceptance(guestFileAt, now)
	checksReady = appendEvidenceCheck(&response, EvaluationCheckAcceptanceEvidence, acceptanceReady) && checksReady
	if checksReady {
		response.Decision = EvaluationDecisionReady
		response.Plan.Applicable = true
	} else {
		response.Decision = EvaluationDecisionIndeterminate
	}
	return response, nil
}

func appendEvidenceCheck(response *EvaluationResponse, name EvaluationCheckName, ready bool) bool {
	if ready {
		response.Checks = append(response.Checks, passedCheck(name, EvaluationClassCapacity))
		return true
	}
	response.Checks = append(response.Checks, unknownCheck(name, EvaluationClassCapacity, EvaluationActionCollectEvidence))
	return false
}

func freshAcceptance(value, now time.Time) bool {
	if value.IsZero() {
		return false
	}
	value = value.UTC()
	return !value.Before(now.Add(-24*time.Hour)) && !value.After(now.Add(30*time.Second))
}

func (s *EvaluationService) selectAgent(ctx context.Context, preferredAgentID string, requiredCapabilities []string, now time.Time) (*controlplane.Agent, *controlplane.AgentInventory) {
	agents, err := s.source.ListAgents(ctx)
	if err != nil || len(agents) > EvaluationMaxAgents {
		return nil, nil
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
	for i := range agents {
		agent := &agents[i]
		if preferredAgentID != "" && agent.ID != preferredAgentID {
			continue
		}
		if agent.ID == "" || len(agent.ID) > 128 || !agent.IsSchedulable() || !evaluationHeartbeatFresh(agent.LastHeartbeat, now) ||
			agent.Protocol != controlplane.AgentProtocolVersion ||
			!containsEvaluationCapabilities(agent.Capabilities, requiredCapabilities) {
			continue
		}
		inventory, err := s.source.GetAgentInventory(ctx, agent.ID)
		if err != nil || inventory == nil || inventory.AgentID != agent.ID ||
			!evaluationInventoryFresh(*inventory, now) ||
			!containsEvaluationCapabilities(inventory.Capabilities, requiredCapabilities) {
			continue
		}
		copy := *agent
		inventoryCopy := *inventory
		return &copy, &inventoryCopy
	}
	return nil, nil
}

func requiredEvaluationCapabilities(topology *graph.Graph) []string {
	required := map[string]bool{}
	for _, node := range topology.All() {
		switch cfg := node.Data.(type) {
		case *config.NodeConfig:
			if substrateName, err := config.ResolveSubstrateRef(cfg.Substrate); err == nil {
				addSubstrateCapabilities(required, substrateName)
			}
		case *config.RouterConfig:
			if substrateName, err := config.ResolveSubstrateRef(cfg.Substrate); err == nil {
				addSubstrateCapabilities(required, substrateName)
			}
		case *config.ImageConfig:
			if substrateName, err := config.ResolveSubstrateRef(cfg.Substrate); err == nil {
				addSubstrateCapabilities(required, substrateName)
			}
		case *config.KernelConfig:
			if substrateName, err := config.ResolveSubstrateRef(cfg.Substrate); err == nil {
				addSubstrateCapabilities(required, substrateName)
			}
		case *config.NetworkConfig:
			if !cfg.NAT {
				required["network"] = true
			}
		case *config.FirewallConfig, *config.SSHAccessConfig:
			required["network"] = true
		}
	}
	return capabilitiesFromSet(required)
}

func containsEvaluationCapabilities(actual, required []string) bool {
	available := make(map[string]struct{}, len(actual))
	for _, capability := range actual {
		available[capability] = struct{}{}
	}
	for _, capability := range required {
		if _, ok := available[capability]; !ok {
			return false
		}
	}
	return true
}

func evaluationInventoryFresh(inventory controlplane.AgentInventory, now time.Time) bool {
	if inventory.Stale || inventory.Status != "fresh" || inventory.ObservedAt.IsZero() {
		return false
	}
	observedAt := inventory.ObservedAt.UTC()
	return !observedAt.Before(now.Add(-2*time.Minute)) && !observedAt.After(now.Add(30*time.Second))
}

func evaluationHeartbeatFresh(heartbeat, now time.Time) bool {
	if heartbeat.IsZero() {
		return false
	}
	value := heartbeat.UTC()
	return !value.Before(now.Add(-2*time.Minute)) && !value.After(now.Add(30*time.Second))
}

func validateEvaluationRequest(request EvaluationRequest) error {
	if request.SchemaVersion != EvaluationSchemaVersion || request.HCL == "" || len(request.HCL) > EvaluationMaxHCLBytes || len(request.PreferredAgentID) > 128 {
		return ErrInvalidEvaluationRequest
	}
	if len(request.ConfigSHA256) != sha256.Size*2 || strings.ToLower(request.ConfigSHA256) != request.ConfigSHA256 {
		return ErrInvalidEvaluationRequest
	}
	if _, err := hex.DecodeString(request.ConfigSHA256); err != nil {
		return ErrInvalidEvaluationRequest
	}
	sum := sha256.Sum256([]byte(request.HCL))
	if hex.EncodeToString(sum[:]) != request.ConfigSHA256 {
		return ErrInvalidEvaluationRequest
	}
	return nil
}

func evaluationProfileAllowed(source string, root *config.Root) bool {
	if len(root.Modules) > 0 || len(root.Data) > 0 || len(root.Resources) > EvaluationMaxResources {
		return false
	}
	file, diagnostics := hclsyntax.ParseConfig([]byte(source), "evaluation.hcl", hcl.Pos{Line: 1, Column: 1})
	if diagnostics.HasErrors() {
		return false
	}
	allowed := true
	hclsyntax.VisitAll(file.Body.(*hclsyntax.Body), func(node hclsyntax.Node) hcl.Diagnostics {
		switch value := node.(type) {
		case *hclsyntax.Attribute:
			if value.Name == "count" || value.Name == "for_each" {
				allowed = false
			}
		case *hclsyntax.FunctionCallExpr:
			if (strings.EqualFold(value.Name, "env") || strings.EqualFold(value.Name, "env_optional")) && !evaluationEnvironmentAllowed(value) {
				allowed = false
			}
		case *hclsyntax.LiteralValueExpr:
			if value.Val.IsKnown() && value.Val.Type() == cty.String && secret.IsReference(value.Val.AsString()) {
				allowed = false
			}
		}
		return nil
	})
	return allowed
}

func evaluationEnvironmentAllowed(call *hclsyntax.FunctionCallExpr) bool {
	if call == nil || len(call.Args) != 1 {
		return false
	}
	value, diagnostics := call.Args[0].Value(nil)
	if diagnostics.HasErrors() || !value.IsKnown() || value.IsNull() || value.Type() != cty.String {
		return false
	}
	_, ok := evaluationInfrastructureEnvironment[value.AsString()]
	return ok
}

func aggregateEvaluationPlan(plan *runtime.Plan) []EvaluationPlanAction {
	counts := map[string]int{}
	for _, change := range plan.Actions {
		key := string(change.Action) + "\x00" + change.Address.Type
		counts[key]++
	}
	result := make([]EvaluationPlanAction, 0, len(counts))
	for key, count := range counts {
		parts := strings.SplitN(key, "\x00", 2)
		result = append(result, EvaluationPlanAction{Action: parts[0], ResourceType: parts[1], Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Action != result[j].Action {
			return result[i].Action < result[j].Action
		}
		return result[i].ResourceType < result[j].ResourceType
	})
	return result
}

func buildEvaluationEnvelope(topology *graph.Graph) (EvaluationEnvelope, bool, bool) {
	envelope := EvaluationEnvelope{Complete: true}
	nodes := map[string]int{}
	for _, node := range topology.All() {
		switch cfg := node.Data.(type) {
		case *config.NodeConfig:
			substrateName, err := config.ResolveSubstrateRef(cfg.Substrate)
			if err == nil {
				nodes[substrateName]++
			}
			envelope.AttachmentCount += len(cfg.Links)
			memory, memoryErr := evaluationMemoryBytes(cfg.Memory)
			if err != nil || cfg.Vcpus <= 0 || memoryErr != nil {
				envelope.Complete = false
				continue
			}
			if cfg.Vcpus > maxInt()-envelope.VCPU {
				envelope.Complete = false
				continue
			}
			envelope.VCPU += cfg.Vcpus
			if memory > math.MaxInt64-envelope.MemoryBytes {
				envelope.Complete = false
				continue
			}
			envelope.MemoryBytes += memory
		case *config.RouterConfig:
			substrateName, err := config.ResolveSubstrateRef(cfg.Substrate)
			if err != nil {
				envelope.Complete = false
				continue
			}
			nodes[substrateName]++
			envelope.AttachmentCount += len(cfg.Interfaces)
			envelope.Complete = false
		case *config.NetworkConfig:
			envelope.NetworkCount++
		case *config.ImageConfig:
			if cfg.Kind != "oci" {
				envelope.ArtifactCount++
			}
		case *config.KernelConfig:
			envelope.ArtifactCount++
		}
	}
	for substrateName, count := range nodes {
		envelope.Nodes = append(envelope.Nodes, EvaluationNodeCount{Substrate: substrateName, Count: count})
	}
	sort.Slice(envelope.Nodes, func(i, j int) bool { return envelope.Nodes[i].Substrate < envelope.Nodes[j].Substrate })
	withinLimits := totalEvaluationNodes(envelope.Nodes) <= EvaluationMaxNodes && envelope.NetworkCount <= EvaluationMaxNetworks &&
		envelope.ArtifactCount <= EvaluationMaxArtifacts && envelope.AttachmentCount <= EvaluationMaxAttachments
	if !withinLimits {
		envelope.Complete = false
	}
	return envelope, envelope.Complete, withinLimits
}

func maxInt() int { return int(^uint(0) >> 1) }

func totalEvaluationNodes(nodes []EvaluationNodeCount) int {
	total := 0
	for _, item := range nodes {
		total += item.Count
	}
	return total
}

func evaluationMemoryBytes(input string) (int64, error) {
	value := strings.TrimSpace(strings.ToUpper(input))
	multiplier := int64(1024 * 1024)
	for suffix, candidate := range map[string]int64{
		"GIB": 1024 * 1024 * 1024, "GB": 1024 * 1024 * 1024, "G": 1024 * 1024 * 1024,
		"MIB": 1024 * 1024, "MB": 1024 * 1024, "M": 1024 * 1024,
	} {
		if strings.HasSuffix(value, suffix) {
			value = strings.TrimSuffix(value, suffix)
			multiplier = candidate
			break
		}
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number <= 0 || number > math.MaxInt64/multiplier {
		return 0, fmt.Errorf("invalid memory")
	}
	return number * multiplier, nil
}

func passedCheck(name EvaluationCheckName, class EvaluationCheckClass) EvaluationCheck {
	return EvaluationCheck{Name: name, State: EvaluationCheckPassed, Class: class, Action: EvaluationActionNone}
}

func failedCheck(name EvaluationCheckName, class EvaluationCheckClass) EvaluationCheck {
	return EvaluationCheck{Name: name, State: EvaluationCheckFailed, Class: class, Action: EvaluationActionFixConfig}
}

func unknownCheck(name EvaluationCheckName, class EvaluationCheckClass, action EvaluationCheckAction) EvaluationCheck {
	return EvaluationCheck{Name: name, State: EvaluationCheckUnknown, Class: class, Action: action}
}
