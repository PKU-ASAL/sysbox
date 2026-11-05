#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/release/lib.sh"
dist="${1:-${repo_root}/dist}"
for command in go jq tar sha256sum strings readelf python3; do require_command "${command}"; done
[[ -s "${dist}/SHA256SUMS" && -s "${dist}/build-metadata.json" ]] || { echo "release: missing checksums or metadata in ${dist}" >&2; exit 1; }
(cd "${dist}" && sha256sum -c SHA256SUMS)

tag="$(jq -er '.tag' "${dist}/build-metadata.json")"
commit="$(jq -er '.commit' "${dist}/build-metadata.json")"
build_time="$(jq -er '.build_time' "${dist}/build-metadata.json")"
validate_release_fingerprint "${dist}/build-metadata.json"
[[ "$(jq -r '.license' "${dist}/build-metadata.json")" == MulanPSL-2.0 ]]
for arch in amd64 arm64; do
  archive="sysbox_${tag}_linux_${arch}.tar.gz"
  expected="$(jq -er --arg arch "${arch}" '.targets[] | select(.os == "linux" and .architecture == $arch) | .sha256' "${dist}/build-metadata.json")"
  actual="$(sha256sum "${dist}/${archive}" | awk '{print $1}')"
  [[ "${actual}" == "${expected}" ]] || { echo "release: metadata checksum mismatch for ${archive}" >&2; exit 1; }
  tmp="$(mktemp -d)"
  # Check every member before extraction: no links, traversal or arbitrary modes.
  python3 - "${dist}/${archive}" "${build_time}" <<'PYARCHIVE'
import datetime,sys,tarfile,pathlib
stamp=int(datetime.datetime.fromisoformat(sys.argv[2].replace('Z','+00:00')).timestamp())
with tarfile.open(sys.argv[1]) as archive:
    seen=set()
    for entry in archive.getmembers():
        path=pathlib.PurePosixPath(entry.name)
        assert entry.name not in seen and not path.is_absolute() and '..' not in path.parts
        seen.add(entry.name)
        assert entry.isfile() or entry.isdir()
        assert entry.uid==entry.gid==0 and entry.mtime==stamp
        assert entry.mode in (0o644,0o755)
    required={'LICENSE','README.md','INSTALL-SHA256SUMS','install.py','build-metadata.json','sysbox','sysbox-init','sysbox-netns','docs/architecture.md','docs/interfaces.md','deploy/systemd/sysbox-host.service','deploy/systemd/sysbox-agent.service','deploy/docker/agent.yaml'}
    assert required<=seen, required-seen
PYARCHIVE
  tar -xzf "${dist}/${archive}" -C "${tmp}"
  (cd "${tmp}" && sha256sum -c INSTALL-SHA256SUMS >/dev/null)
  cmp deploy/install.py "${tmp}/install.py"
  cmp deploy/systemd/sysbox-host.service "${tmp}/deploy/systemd/sysbox-host.service"
  cmp deploy/systemd/sysbox-agent.service "${tmp}/deploy/systemd/sysbox-agent.service"
  expected_sysbox_sha="$(jq -er --arg arch "${arch}" '.targets[] | select(.os == "linux" and .architecture == $arch) | .sysbox_sha256' "${dist}/build-metadata.json")"
  expected_init_sha="$(jq -er --arg arch "${arch}" '.targets[] | select(.os == "linux" and .architecture == $arch) | .sysbox_init_sha256' "${dist}/build-metadata.json")"
  [[ "$(sha256sum "${tmp}/sysbox" | awk '{print $1}')" == "${expected_sysbox_sha}" ]] || { echo "release: sysbox binary checksum mismatch for ${arch}" >&2; rm -rf "${tmp}"; exit 1; }
  [[ "$(sha256sum "${tmp}/sysbox-init" | awk '{print $1}')" == "${expected_init_sha}" ]] || { echo "release: sysbox-init binary checksum mismatch for ${arch}" >&2; rm -rf "${tmp}"; exit 1; }
  expected_netns_sha="$(jq -er --arg arch "${arch}" '.targets[] | select(.os == "linux" and .architecture == $arch) | .sysbox_netns_sha256' "${dist}/build-metadata.json")"
  [[ "$(sha256sum "${tmp}/sysbox-netns" | awk '{print $1}')" == "${expected_netns_sha}" ]] || { echo "release: sysbox-netns checksum mismatch for ${arch}" >&2; exit 1; }
  jq -e --arg tag "${tag}" --arg commit "${commit}" --arg build_time "${build_time}" --arg arch "${arch}" \
    '.version == $tag and .commit == $commit and .build_time == $build_time and .architecture == $arch and .license == "MulanPSL-2.0"' \
    "${tmp}/build-metadata.json" >/dev/null
  cmp LICENSE "${tmp}/LICENSE"
  cmp README.md "${tmp}/README.md"
  machine="$(readelf -h "${tmp}/sysbox" | awk -F: '/Machine:/ {sub(/^[[:space:]]+/, "", $2); print $2}')"
  init_machine="$(readelf -h "${tmp}/sysbox-init" | awk -F: '/Machine:/ {sub(/^[[:space:]]+/, "", $2); print $2}')"
  case "${arch}" in
    amd64) expected_machine='Advanced Micro Devices X86-64' ;;
    arm64) expected_machine='AArch64' ;;
  esac
  [[ "${machine}" == "${expected_machine}" && "${init_machine}" == "${expected_machine}" ]] || { echo "release: ELF architecture mismatch for ${arch}" >&2; rm -rf "${tmp}"; exit 1; }
  strings "${tmp}/sysbox" | grep -F "${commit}" >/dev/null
  strings "${tmp}/sysbox" | grep -F "${build_time}" >/dev/null
  if [[ "$(go env GOOS)/$(go env GOARCH)" == "linux/${arch}" ]]; then
    "${tmp}/sysbox" version --json | jq -e --arg tag "${tag}" --arg commit "${commit}" --arg build_time "${build_time}" \
      '.version == $tag and .commit == $commit and .build_time == $build_time' >/dev/null
  fi
  rm -rf "${tmp}"
done
windows_archive="sysbox_${tag}_windows_amd64.tar.gz"
python3 - "${dist}" "${windows_archive}" <<'PYWINDOWS'
import hashlib,json,pathlib,struct,sys,tarfile
root=pathlib.Path(sys.argv[1]); metadata=json.loads((root/'build-metadata.json').read_text())
entries=[t for t in metadata['targets'] if t['os']=='windows' and t['architecture']=='amd64']
assert len(entries)==1, 'expected one Windows target'
target=entries[0]; archive=root/sys.argv[2]
assert target['archive']==archive.name and hashlib.sha256(archive.read_bytes()).hexdigest()==target['sha256']
with tarfile.open(archive) as tar:
    members=tar.getmembers()
    assert [m.name for m in members]==['build-metadata.json','install.ps1','sysbox-windows-task.exe']
    assert all(m.isfile() and m.uid==m.gid==0 for m in members)
    data={m.name:tar.extractfile(m).read() for m in members}
info=json.loads(data['build-metadata.json'])
assert info['commit']==metadata['commit'] and info['version']==metadata['tag'] and info['build_time']==metadata['build_time']
assert hashlib.sha256(data['install.ps1']).hexdigest()==target['installer_sha256']
exe=data['sysbox-windows-task.exe']
assert hashlib.sha256(exe).hexdigest()==target['helper_sha256']
assert exe[:2]==b'MZ'
offset=struct.unpack_from('<I',exe,0x3c)[0]
assert exe[offset:offset+4]==b'PE\0\0' and struct.unpack_from('<H',exe,offset+4)[0]==0x8664
assert metadata['commit'].encode() in exe, 'Windows helper lacks source commit'
PYWINDOWS
echo "release: verified ${tag} artifacts"
