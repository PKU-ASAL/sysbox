package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: workflowcheck <ci|acceptance|release> FILE")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[2])
	if err == nil {
		err = validateWorkflow(os.Args[1], raw)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func validateWorkflow(kind string, raw []byte) error {
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("decode workflow: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("workflow root must be a mapping")
	}
	root := document.Content[0]
	on := mappingValue(root, "on")
	jobs := mappingValue(root, "jobs")
	if on == nil || jobs == nil || jobs.Kind != yaml.MappingNode {
		return fmt.Errorf("workflow requires on and jobs mappings")
	}
	if err := validatePinnedActions(root); err != nil {
		return err
	}

	switch kind {
	case "ci":
		if !mappingHasOnly(on, "push", "pull_request") || mappingValue(on, "push") == nil || mappingValue(on, "pull_request") == nil {
			return fmt.Errorf("CI must trigger only for push and pull_request")
		}
		if bytes.Contains(raw, []byte("secrets.")) {
			return fmt.Errorf("CI must not reference a secret")
		}
	case "acceptance":
		if !mappingHasOnly(on, "workflow_dispatch") || mappingValue(on, "workflow_dispatch") == nil {
			return fmt.Errorf("acceptance must trigger only by workflow_dispatch")
		}
		job := mappingValue(jobs, "acceptance")
		if job == nil || !nodeContains(job, "release") || !nodeContains(job, "self-hosted") {
			return fmt.Errorf("acceptance requires the trusted release runner")
		}
	case "release":
		if !mappingHasOnly(on, "push") {
			return fmt.Errorf("release must trigger only for tag push")
		}
		push := mappingValue(on, "push")
		if push == nil || !nodeContains(mappingValue(push, "tags"), "v*.*.*") {
			return fmt.Errorf("release tag trigger must be v*.*.*")
		}
		publish := mappingValue(jobs, "publish")
		if publish == nil || !nodeContains(publish, "RELEASE_TOKEN") {
			return fmt.Errorf("publish job must receive RELEASE_TOKEN")
		}
		if nodeContains(mappingValue(publish, "env"), "RELEASE_TOKEN") {
			return fmt.Errorf("RELEASE_TOKEN must not be job-wide")
		}
		for i := 0; i < len(jobs.Content); i += 2 {
			name, job := jobs.Content[i].Value, jobs.Content[i+1]
			if name != "publish" && nodeContains(job, "RELEASE_TOKEN") {
				return fmt.Errorf("RELEASE_TOKEN is scoped outside publish job")
			}
		}
		serialized, _ := yaml.Marshal(publish)
		buildIndex := bytes.Index(serialized, []byte("scripts/release/build.sh"))
		ociIndex := bytes.Index(serialized, []byte("scripts/release/oci.sh build"))
		forgejoIndex := bytes.Index(serialized, []byte("scripts/release/forgejo.sh publish"))
		if buildIndex < 0 || ociIndex <= buildIndex || forgejoIndex <= ociIndex {
			return fmt.Errorf("publish steps must order build, OCI, then Forgejo release")
		}
	default:
		return fmt.Errorf("unknown workflow kind %q", kind)
	}
	return nil
}

func validatePinnedActions(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Value == "uses" {
				parts := strings.Split(node.Content[i+1].Value, "@")
				if len(parts) != 2 || len(parts[1]) != 40 || strings.Trim(parts[1], "0123456789abcdef") != "" {
					return fmt.Errorf("third-party action %q must be pinned by full commit SHA", node.Content[i+1].Value)
				}
			}
		}
	}
	for _, child := range node.Content {
		if err := validatePinnedActions(child); err != nil {
			return err
		}
	}
	return nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func mappingHasOnly(node *yaml.Node, keys ...string) bool {
	if node == nil || node.Kind != yaml.MappingNode {
		return false
	}
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
	}
	for i := 0; i < len(node.Content); i += 2 {
		if !allowed[node.Content[i].Value] {
			return false
		}
	}
	return true
}

func nodeContains(node *yaml.Node, value string) bool {
	if node == nil {
		return false
	}
	if strings.Contains(node.Value, value) {
		return true
	}
	for _, child := range node.Content {
		if nodeContains(child, value) {
			return true
		}
	}
	return false
}
