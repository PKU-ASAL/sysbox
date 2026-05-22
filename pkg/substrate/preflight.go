package substrate

import (
	"os"
	"os/exec"
	"strings"
)

type PreflightCheck struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Severity string `json:"severity"`
	Message  string `json:"message,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

func DockerPreflightChecks(_ bool) []PreflightCheck {
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		return []PreflightCheck{{Name: "docker_socket", OK: false, Severity: "error", Message: err.Error(), Hint: "mount /var/run/docker.sock into the API container"}}
	}
	if err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Run(); err != nil {
		return []PreflightCheck{{Name: "docker_daemon", OK: false, Severity: "error", Message: err.Error(), Hint: "ensure the API process can access the Docker daemon"}}
	}
	return []PreflightCheck{{Name: "docker_daemon", OK: true, Severity: "info", Message: "Docker daemon reachable"}}
}

func FirecrackerPreflightChecks(required bool) []PreflightCheck {
	return append(KVMPreflightCheck(required), ToolPreflightCheck("firecracker", required)...)
}

func LibvirtPreflightChecks(required bool) []PreflightCheck {
	if _, err := os.Stat("/var/run/libvirt"); err != nil {
		severity := "warning"
		if required {
			severity = "error"
		}
		return []PreflightCheck{{Name: "libvirt_socket", OK: false, Severity: severity, Message: err.Error(), Hint: "mount /var/run/libvirt when using libvirt substrate"}}
	}
	return []PreflightCheck{{Name: "libvirt_socket", OK: true, Severity: "info", Message: "/var/run/libvirt present"}}
}

func KVMPreflightCheck(required bool) []PreflightCheck {
	if _, err := os.Stat("/dev/kvm"); err != nil {
		severity := "warning"
		if required {
			severity = "error"
		}
		return []PreflightCheck{{Name: "kvm_device", OK: false, Severity: severity, Message: err.Error(), Hint: "mount /dev/kvm and run with sufficient privileges"}}
	}
	return []PreflightCheck{{Name: "kvm_device", OK: true, Severity: "info", Message: "/dev/kvm present"}}
}

func ToolPreflightCheck(tool string, required bool) []PreflightCheck {
	if p := explicitToolPath(tool); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return []PreflightCheck{{Name: tool + "_bin", OK: true, Severity: "info", Message: p}}
		}
	}
	if p, err := exec.LookPath(tool); err == nil {
		return []PreflightCheck{{Name: tool + "_bin", OK: true, Severity: "info", Message: p}}
	}
	severity := "warning"
	if required {
		severity = "error"
	}
	return []PreflightCheck{{Name: tool + "_bin", OK: false, Severity: severity, Message: tool + " not found", Hint: "mount it explicitly and set SYSBOX_" + strings.ToUpper(tool) + "_BIN"}}
}

func explicitToolPath(tool string) string {
	if tool == "firecracker" {
		if p := os.Getenv("SYSBOX_FIRECRACKER_BIN"); p != "" {
			return p
		}
	}
	return ""
}
