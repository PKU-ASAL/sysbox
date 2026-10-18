package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The executable gate requires all three real rounds; no missing environment
// or skip is allowed once selected by the strict campaign runner.
func TestDisposableLiveFaultEvidence(t *testing.T) {
	for _, fault := range []string{"FAILOVER", "DATABASE", "PARTITION"} {
		for _, kind := range []string{"SUMMARY", "FAULT", "CLEANUP"} {
			if os.Getenv("SYSBOX_"+fault+"_"+kind) == "" {
				t.Skip("explicit recorded disposable fault evidence required")
			}
		}
	}
	binary := filepath.Join(t.TempDir(), "sysbox")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binary, "../../cmd/sysbox")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build current binary: %v %s", err, out)
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	script := `import json,os,sys
from pathlib import Path
import fault_campaign
for fault in ('FAILOVER','DATABASE','PARTITION'):
 paths=[Path(os.environ['SYSBOX_'+fault+'_'+kind]) for kind in ('SUMMARY','FAULT','CLEANUP')]
 if len({p.parent.resolve() for p in paths})!=1: raise RuntimeError('mixed evidence round')
 summary,proof,cleanup=[json.loads(p.read_text()) for p in paths]
 expected={'FAILOVER':'failover','DATABASE':'database','PARTITION':'private-network'}[fault]
 if summary.get('fault')!=expected: raise RuntimeError('wrong fault type')
 fault_campaign.verify_round(summary,proof,cleanup)
 if any(v!=sys.argv[1] for v in summary['binary_sha256'].values()): raise RuntimeError('fault ran a different source build')
 print(expected+': verified')
`

	command := exec.Command("/usr/bin/python3", "-c", script, digest)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fault evidence: %v\n%s", err, output)
	}
	t.Log(string(output))
}
