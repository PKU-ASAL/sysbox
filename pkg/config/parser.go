package config

import (
	"fmt"
	"os"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
)

// ParseFile parses an HCL file into the Root structure.
//
// Note: this does a shallow first-pass decode of substrate/resource blocks.
// Inner fields (NodeConfig, NetworkConfig, etc.) are decoded on demand
// using DecodeResource() because different resource types have different
// schemas.
func ParseFile(path string) (*Root, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read HCL file: %w", err)
	}

	parser := hclparse.NewParser()
	file, diag := parser.ParseHCL(data, path)
	if diag.HasErrors() {
		return nil, fmt.Errorf("parse HCL: %s", diag.Error())
	}

	var root Root
	if diag := gohcl.DecodeBody(file.Body, nil, &root); diag.HasErrors() {
		return nil, fmt.Errorf("decode HCL: %s", diag.Error())
	}
	return &root, nil
}

// DecodeResource decodes a resource block's inner fields into the given
// target struct (e.g. *NodeConfig, *NetworkConfig). Caller picks the target
// based on r.Type. Pass an EvalContext (from BuildEvalContext) to enable
// bare-identifier traversals like substrate.docker.light or
// sysbox_image.alpine.id; pass nil for legacy quoted-string refs.
func DecodeResource(r *ResourceBlock, target any, ctx *hcl.EvalContext) error {
	if r.Remain == nil {
		return fmt.Errorf("resource %s.%s: empty body", r.Type, r.Name)
	}
	if diag := gohcl.DecodeBody(r.Remain, ctx, target); diag.HasErrors() {
		return fmt.Errorf("decode resource %s.%s: %s", r.Type, r.Name, diag.Error())
	}
	return nil
}
