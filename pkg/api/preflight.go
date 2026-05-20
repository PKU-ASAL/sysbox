package api

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/config"
)

type preflightCheck struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Severity string `json:"severity"` // error | warning | info
	Message  string `json:"message,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

type preflightResult struct {
	OK     bool             `json:"ok"`
	Checks []preflightCheck `json:"checks"`
}

func (r *preflightResult) add(name string, ok bool, severity, message, hint string) {
	r.Checks = append(r.Checks, preflightCheck{
		Name:     name,
		OK:       ok,
		Severity: severity,
		Message:  message,
		Hint:     hint,
	})
	if !ok && severity == "error" {
		r.OK = false
	}
}

func (r *preflightResult) err() error {
	if r.OK {
		return nil
	}
	for _, c := range r.Checks {
		if !c.OK && c.Severity == "error" {
			if c.Hint != "" {
				return fmt.Errorf("preflight failed: %s: %s (%s)", c.Name, c.Message, c.Hint)
			}
			return fmt.Errorf("preflight failed: %s: %s", c.Name, c.Message)
		}
	}
	return fmt.Errorf("preflight failed")
}

// GET /v1/capabilities
func (s *Server) handleCapabilities(w http.ResponseWriter, _ *http.Request) {
	res := preflightResult{OK: true}
	addDockerCheck(&res)
	addKVMCheck(&res, false)
	addToolCheck(&res, "firecracker", false)
	addLibvirtCheck(&res, false)
	writeJSON(w, http.StatusOK, res)
}

// GET /v1/topologies/{topology}/preflight
func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.preflightTopology(topology)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) preflightTopology(topology string) (*preflightResult, error) {
	root, err := config.ParseFile(s.hclFile(topology))
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	ctx := config.BuildEvalContext(root, filepath.Dir(s.hclFile(topology)))
	res := &preflightResult{OK: true}

	needsDocker := false
	needsFirecracker := false
	needsLibvirt := false
	for _, sb := range root.Substrates {
		switch sb.Type {
		case "docker":
			needsDocker = true
		case "firecracker":
			needsFirecracker = true
		case "libvirt":
			needsLibvirt = true
		}
	}

	if needsDocker {
		addDockerCheck(res)
	}
	if needsFirecracker {
		addKVMCheck(res, true)
		addToolCheck(res, "firecracker", true)
	}
	if needsLibvirt {
		addLibvirtCheck(res, true)
	}

	for _, rb := range root.Resources {
		switch rb.Type {
		case "sysbox_image":
			cfg := &config.ImageConfig{}
			if err := config.DecodeResource(&rb, cfg, ctx); err != nil {
				res.add("resource:"+rb.Type+"."+rb.Name, false, "error", err.Error(), "fix the HCL decode error")
				continue
			}
			addArtifactCheck(res, "image:"+rb.Name+":rootfs", cfg.Rootfs, cfg.SHA256)
			addArtifactCheck(res, "image:"+rb.Name+":qcow2", cfg.QCow2, cfg.SHA256)
		case "sysbox_kernel":
			cfg := &config.KernelConfig{}
			if err := config.DecodeResource(&rb, cfg, ctx); err != nil {
				res.add("resource:"+rb.Type+"."+rb.Name, false, "error", err.Error(), "fix the HCL decode error")
				continue
			}
			addArtifactCheck(res, "kernel:"+rb.Name, cfg.Source, cfg.SHA256)
		}
	}

	return res, nil
}

func addDockerCheck(res *preflightResult) {
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		res.add("docker_socket", false, "error", err.Error(), "mount /var/run/docker.sock into the API container")
		return
	}
	if err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Run(); err != nil {
		res.add("docker_daemon", false, "error", err.Error(), "ensure the API process can access the Docker daemon")
		return
	}
	res.add("docker_daemon", true, "info", "Docker daemon reachable", "")
}

func addKVMCheck(res *preflightResult, required bool) {
	if _, err := os.Stat("/dev/kvm"); err != nil {
		severity := "warning"
		if required {
			severity = "error"
		}
		res.add("kvm_device", false, severity, err.Error(), "mount /dev/kvm and run with sufficient privileges")
		return
	}
	res.add("kvm_device", true, "info", "/dev/kvm present", "")
}

func addToolCheck(res *preflightResult, tool string, required bool) {
	if p := explicitToolPath(tool); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			res.add(tool+"_bin", true, "info", p, "")
			return
		}
	}
	if p, err := exec.LookPath(tool); err == nil {
		res.add(tool+"_bin", true, "info", p, "")
		return
	}
	severity := "warning"
	if required {
		severity = "error"
	}
	res.add(tool+"_bin", false, severity, tool+" not found", "mount it into SYSBOX_TOOL_DIR or set SYSBOX_"+upperToolEnv(tool)+"_BIN")
}

func explicitToolPath(tool string) string {
	if tool == "firecracker" {
		if p := os.Getenv("SYSBOX_FIRECRACKER_BIN"); p != "" {
			return p
		}
		if p := os.Getenv("SYSBOX_FC_BIN"); p != "" {
			return p
		}
	}
	if dir := os.Getenv("SYSBOX_TOOL_DIR"); dir != "" {
		return filepath.Join(dir, tool)
	}
	return ""
}

func upperToolEnv(tool string) string {
	if tool == "firecracker" {
		return "FIRECRACKER"
	}
	return tool
}

func addLibvirtCheck(res *preflightResult, required bool) {
	if _, err := os.Stat("/var/run/libvirt"); err != nil {
		severity := "warning"
		if required {
			severity = "error"
		}
		res.add("libvirt_socket", false, severity, err.Error(), "mount /var/run/libvirt when using libvirt substrate")
		return
	}
	res.add("libvirt_socket", true, "info", "/var/run/libvirt present", "")
}

func addArtifactCheck(res *preflightResult, name, source, sha string) {
	if source == "" {
		return
	}
	if artifact.IsURL(source) {
		msg := "remote artifact will be fetched on demand"
		if sha == "" {
			res.add(name, true, "warning", msg, "set sha256 for reproducible artifact caching")
			return
		}
		res.add(name, true, "info", msg, "")
		return
	}
	p := source
	if len(p) >= 7 && p[:7] == "file://" {
		p = p[7:]
	}
	if !filepath.IsAbs(p) {
		abs, err := filepath.Abs(p)
		if err == nil {
			p = abs
		}
	}
	if st, err := os.Stat(p); err != nil {
		res.add(name, false, "error", err.Error(), "mount the artifact into the API container or use a URL source")
	} else if st.IsDir() {
		res.add(name, false, "error", p+" is a directory", "point the HCL field at a file")
	} else {
		res.add(name, true, "info", p, "")
	}
}
