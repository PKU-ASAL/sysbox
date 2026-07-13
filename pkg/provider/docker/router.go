package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) ConfigureNAT(ctx context.Context, handle substrate.NodeHandle, fromReq driver.AttachmentRequest, from driver.AttachmentResult, toReq driver.AttachmentRequest, to driver.AttachmentResult) error {
	fromIf, err := s.resolveAttachmentDevice(ctx, handle, fromReq, from)
	if err != nil {
		return err
	}
	toIf, err := s.resolveAttachmentDevice(ctx, handle, toReq, to)
	if err != nil {
		return err
	}
	bindings := map[string]string{fromReq.Name: fromIf, toReq.Name: toIf}
	targetState, err := json.Marshal(dockerPolicyTarget{ContainerID: handle.ID, Bindings: bindings})
	if err != nil {
		return err
	}
	spec := driver.RulesetSpec{
		Owner: "docker-router/" + handle.ID, Family: driver.FamilyIPv4,
		DefaultInput: driver.VerdictAccept, DefaultOutput: driver.VerdictAccept, DefaultForward: driver.VerdictDrop,
		Rules: []driver.PolicyRule{
			{ID: "nat-forward", Direction: driver.DirectionForward, InputAttachment: fromReq.Name, OutputAttachment: toReq.Name, Protocol: driver.ProtocolAll, Verdict: driver.VerdictAccept, Counter: true},
			{ID: "nat-return", Direction: driver.DirectionForward, InputAttachment: toReq.Name, OutputAttachment: fromReq.Name, Protocol: driver.ProtocolAll, States: []driver.ConnectionState{driver.StateEstablished, driver.StateRelated}, Verdict: driver.VerdictAccept, Counter: true},
		},
		NAT: &driver.NATPolicy{SourceAttachment: fromReq.Name, UplinkAttachment: toReq.Name, SourceCIDRs: append([]string(nil), fromReq.IPPrefixes...), Masquerade: true},
	}
	_, err = s.ApplyRuleset(ctx, driver.PolicyTarget{Resource: handle.ID, State: targetState}, spec)
	return err
}

func (s *Substrate) resolveAttachmentDevice(ctx context.Context, handle substrate.NodeHandle, req driver.AttachmentRequest, result driver.AttachmentResult) (string, error) {
	if result.GuestDevice != "" {
		return result.GuestDevice, nil
	}
	if len(req.IPPrefixes) == 0 {
		return "", fmt.Errorf("attachment %q has no observed device or IP", req.Name)
	}
	ip := strings.SplitN(req.IPPrefixes[0], "/", 2)[0]
	command := fmt.Sprintf(`ip -o addr show | awk '$4 ~ /^%s\// {print $2; exit}'`, ip)
	resolved, err := s.ExecInNode(ctx, handle, substrate.ExecSpec{Cmd: []string{"sh", "-c", command}})
	if err != nil {
		return "", fmt.Errorf("resolve attachment %q: %w", req.Name, err)
	}
	device := strings.TrimSpace(resolved.Stdout)
	if device == "" {
		return "", fmt.Errorf("resolve attachment %q: no interface has IP %s", req.Name, ip)
	}
	return device, nil
}
