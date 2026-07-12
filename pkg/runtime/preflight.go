package runtime

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/substrate"
)

type ResourcePreflightProvider interface {
	PreflightResource(r config.ResourceBlock, ctx *hcl.EvalContext) []substrate.PreflightCheck
}

func ResourcePreflightChecks(r config.ResourceBlock, ctx *hcl.EvalContext) []substrate.PreflightCheck {
	p, ok := GetResourceHandler(r.Type)
	if !ok {
		return nil
	}
	hook, ok := p.(ResourcePreflightProvider)
	if !ok {
		return nil
	}
	return hook.PreflightResource(r, ctx)
}

func ArtifactPreflightCheck(name, source, sha string) *substrate.PreflightCheck {
	if source == "" {
		return nil
	}
	if artifact.IsURL(source) {
		msg := "remote artifact will be fetched on demand"
		if sha == "" {
			return &substrate.PreflightCheck{Name: name, OK: true, Severity: "warning", Message: msg, Hint: "set sha256 for reproducible artifact caching"}
		}
		return &substrate.PreflightCheck{Name: name, OK: true, Severity: "info", Message: msg}
	}
	p := source
	if len(p) >= 7 && p[:7] == "file://" {
		p = p[7:]
	}
	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
	}
	if st, err := os.Stat(p); err != nil {
		return &substrate.PreflightCheck{Name: name, OK: false, Severity: "error", Message: err.Error(), Hint: "mount the artifact into the API container or use a URL source"}
	} else if st.IsDir() {
		return &substrate.PreflightCheck{Name: name, OK: false, Severity: "error", Message: p + " is a directory", Hint: "point the HCL field at a file"}
	}
	return &substrate.PreflightCheck{Name: name, OK: true, Severity: "info", Message: p}
}

func DecodePreflightError(resourceType, name string, err error) substrate.PreflightCheck {
	return substrate.PreflightCheck{
		Name:     fmt.Sprintf("resource:%s.%s", resourceType, name),
		OK:       false,
		Severity: "error",
		Message:  err.Error(),
		Hint:     "fix the HCL decode error",
	}
}
