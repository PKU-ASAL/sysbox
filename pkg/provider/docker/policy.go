package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/docker/docker/errdefs"

	"github.com/oslab/sysbox/pkg/driver"
	networkprovider "github.com/oslab/sysbox/pkg/provider/network"
)

type dockerPolicyTarget struct {
	ContainerID string            `json:"container_id"`
	Bindings    map[string]string `json:"bindings"`
}

func decodeDockerPolicyTarget(target driver.PolicyTarget) (dockerPolicyTarget, error) {
	var state dockerPolicyTarget
	if err := json.Unmarshal(target.State, &state); err != nil {
		return state, driver.Wrap(driver.ErrorInvalidState, "docker", "decode policy target", err)
	}
	if state.ContainerID == "" {
		return state, driver.Wrap(driver.ErrorInvalidState, "docker", "policy target container_id is required", nil)
	}
	if state.Bindings == nil {
		state.Bindings = map[string]string{}
	}
	return state, nil
}

func (s *Substrate) ApplyRuleset(ctx context.Context, target driver.PolicyTarget, spec driver.RulesetSpec) (driver.RulesetObservation, error) {
	state, err := decodeDockerPolicyTarget(target)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	var observation driver.RulesetObservation
	err = s.withContainerNetNS(ctx, state.ContainerID, func(fd int) error {
		var applyErr error
		observation, applyErr = networkprovider.ApplyRulesetInNetNSFD(fd, spec, state.Bindings)
		return applyErr
	})
	if err != nil {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorUnavailable, "docker", "apply ruleset", err)
	}
	return observation, nil
}

func (s *Substrate) ObserveRuleset(ctx context.Context, target driver.PolicyTarget, owner string) (driver.RulesetObservation, error) {
	state, err := decodeDockerPolicyTarget(target)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	var observation driver.RulesetObservation
	err = s.withContainerNetNS(ctx, state.ContainerID, func(fd int) error {
		var observeErr error
		observation, observeErr = networkprovider.ObserveRulesetInNetNSFD(fd, owner)
		return observeErr
	})
	return observation, err
}

func (s *Substrate) DeleteRuleset(ctx context.Context, target driver.PolicyTarget, owner string) error {
	state, err := decodeDockerPolicyTarget(target)
	if err != nil {
		return err
	}
	return s.withContainerNetNS(ctx, state.ContainerID, func(fd int) error {
		return networkprovider.DeleteRulesetInNetNSFD(fd, owner)
	})
}

func (s *Substrate) withContainerNetNS(ctx context.Context, containerID string, fn func(int) error) error {
	container, err := s.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		category := driver.ErrorUnavailable
		if errdefs.IsNotFound(err) {
			category = driver.ErrorNotFound
		}
		return driver.Wrap(category, "docker", "inspect policy target", err)
	}
	if container.State == nil || container.State.Pid == 0 {
		return fmt.Errorf("policy target container %s is not running", containerID)
	}
	ns, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", container.State.Pid))
	if err != nil {
		return fmt.Errorf("open container network namespace: %w", err)
	}
	defer ns.Close()
	return fn(int(ns.Fd()))
}
