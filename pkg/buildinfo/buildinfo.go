package buildinfo

import (
	"fmt"
	"runtime"
)

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
}

func Current() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildTime: BuildTime,
		GoVersion: runtime.Version(),
	}
}

// Unstamped reports a source build without release metadata. It is explicitly
// distinguishable from a missing identity and never matches a stamped build.
func (i Info) Unstamped() bool { return i.Version == "dev" && i.Commit == "unknown" }

// CheckCompatible implements the current deployment contract: one version and
// immutable commit across API, worker and host. It is not an authenticity proof.
func CheckCompatible(local, peer Info) error {
	if local.Unstamped() && peer.Unstamped() {
		return nil
	}
	valid := func(i Info) bool {
		if i.Version == "" || len(i.Commit) != 40 {
			return false
		}
		for _, c := range i.Commit {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
		return true
	}
	if !valid(local) || !valid(peer) || local.Commit != peer.Commit || local.Version != peer.Version {
		return fmt.Errorf("build mismatch: local %s@%s, peer %s@%s; install matching release artifacts", local.Version, local.Commit, peer.Version, peer.Commit)
	}
	return nil
}
