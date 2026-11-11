#!/usr/bin/env bash
# Exercise public build targets in an isolated, clean clone. Never reset or
# clean the caller's tree to make the release cleanliness gate pass.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/release/lib.sh"
cd "${repo_root}"
require_clean_source
scratch="$(mktemp -d /tmp/sysbox-clean-build-test.XXXXXX)"
trap 'rm -rf "${scratch}"' EXIT
git clone --quiet --no-hardlinks --no-local "${repo_root}" "${scratch}/source"
git -C "${scratch}/source" checkout --quiet --detach "$(git rev-parse HEAD)"
cd "${scratch}/source"
make build build-all
make build BINARY=bin/custom/sysbox-cli
test -x bin/custom/sysbox-cli
test -x bin/custom/sysbox-init
test -x bin/custom/sysbox-netns
require_clean_source
# Both bundles must embed both freshly built helpers, not tracked placeholders.
python3 - <<'PY'
from pathlib import Path
import struct
helpers = {arch: Path('bin/linux-'+arch+'/sysbox-init').read_bytes() for arch in ('amd64', 'arm64')}
for arch, machine in [('amd64', 62), ('arm64', 183)]:
    bundle = Path('bin/linux-'+arch)
    for name in ('sysbox', 'sysbox-init', 'sysbox-netns'):
        raw = (bundle/name).read_bytes()
        assert raw[:4] == b'\x7fELF' and struct.unpack_from('<H', raw, 18)[0] == machine, (arch, name)
    cli = (bundle/'sysbox').read_bytes()
    for helper in helpers.values():
        assert len(helper)>1024 and helper in cli, 'CLI does not embed matching guest helper'
for helper in helpers.values():
    assert helper in Path('bin/sysbox').read_bytes(), 'host CLI embeds stale helper'
PY
# Exercise an actual Go test binary and verify wrapper propagation to child Go
# commands without invoking privileged test entry points.
cat > pkg/provider/firecracker/initbin/overlay_contract_test.go <<'GO'
package initbin
import "testing"
func TestOverlayContract(t *testing.T) {
 for _,arch:=range []string{"amd64","arm64"}{
  raw,err:=BytesFor(arch)
  if err!=nil||len(raw)<1024{t.Fatalf("%s helper missing: %v",arch,err)}
 }
}
GO
bash scripts/release/with-helper-overlay.sh bash -c '"${GO:-go}" test ./pkg/provider/firecracker/initbin -run TestOverlayContract -count=1'
rm pkg/provider/firecracker/initbin/overlay_contract_test.go
require_clean_source
# Keep the rejection tests: tracked edits and untracked files remain forbidden.
printf '\n' >> README.md
if require_clean_source >/dev/null 2>&1; then echo 'accepted modified source' >&2; exit 1; fi
git restore README.md
printf 'package bad\n' > untracked.go
if require_clean_source >/dev/null 2>&1; then echo 'accepted untracked source' >&2; exit 1; fi
rm untracked.go
require_clean_source
echo 'Clean build and helper embedding contracts passed.'
