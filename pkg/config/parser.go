package config

import (
	"fmt"
	"os"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"

	"github.com/oslab/sysbox/pkg/address"
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
	return ParseString(string(data), path)
}

// ParseString parses HCL text into the Root structure. The srcLabel is
// used in diagnostics (e.g. ".hcl" for API-submitted content).
func ParseString(src, srcLabel string) (*Root, error) {
	parser := hclparse.NewParser()
	file, diag := parser.ParseHCL([]byte(src), srcLabel)
	if diag.HasErrors() {
		diagnostics := fromHCLDiagnostics(diag)
		diagnostics.Sort()
		return nil, diagnostics
	}

	var root Root
	if diag := gohcl.DecodeBody(file.Body, nil, &root); diag.HasErrors() {
		diagnostics := fromHCLDiagnostics(diag)
		diagnostics.Sort()
		return nil, diagnostics
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
		diagnostics := fromHCLDiagnostics(diag)
		resourceAddress := address.Resource(r.Type, r.Name)
		for i := range diagnostics {
			diagnostics[i].Address = &resourceAddress
		}
		diagnostics.Sort()
		return diagnostics
	}
	return nil
}
