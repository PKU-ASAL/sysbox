#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

fail() {
  printf 'windows tools test: %s\n' "$*" >&2
  exit 1
}

assert_contains() {
  local file="$1" expected="$2"
  grep -Fq -- "${expected}" "${file}" || fail "${file} does not contain ${expected}"
}

assert_not_contains() {
  local file="$1" unexpected="$2"
  if grep -Fq -- "${unexpected}" "${file}"; then
    fail "${file} contains ${unexpected}"
  fi
}

password='Str0ng<&"Password!'
answer="${work}/answer/Autounattend.xml"
WINDOWS_ADMIN_PASSWORD="${password}" \
  "${root}/scripts/windows/render-unattend.sh" "${answer}"

[[ "$(stat -c '%a' "${answer}")" == "600" ]] || fail "answer file must use mode 600"
assert_contains "${answer}" 'Str0ng&lt;&amp;&quot;Password!'
assert_not_contains "${answer}" '__WINDOWS_ADMIN_PASSWORD__'
assert_contains "${answer}" '<Value>Windows 10 Pro</Value>'
assert_contains "${answer}" '<ProductKey><Key>W269N-WFGWX-YVC9B-4J6C9-T83GX</Key><WillShowUI>Never</WillShowUI></ProductKey>'
assert_contains "${answer}" 'E:\viostor\w10\amd64'
assert_contains "${answer}" 'C:\sysbox-image-build.complete'

if WINDOWS_ADMIN_PASSWORD='short' "${root}/scripts/windows/render-unattend.sh" "${work}/short.xml" 2>"${work}/short.err"; then
  fail "weak password was accepted"
fi
assert_contains "${work}/short.err" 'WINDOWS_ADMIN_PASSWORD must be at least 12 characters'

if WINDOWS_ADMIN_PASSWORD='alllowercasepassword' "${root}/scripts/windows/render-unattend.sh" "${work}/simple.xml" 2>"${work}/simple.err"; then
  fail "password without Windows complexity was accepted"
fi
assert_contains "${work}/simple.err" 'must contain characters from at least three of four classes'

domain="${work}/domain.xml"
"${root}/scripts/windows/render-domain.sh" \
  "sysbox-build-windows10" \
  "${work}/windows.iso" \
  "${work}/virtio-win.iso" \
  "${work}/answer.iso" \
  "${work}/windows10.qcow2" \
  >"${domain}"

assert_contains "${domain}" '<name>sysbox-build-windows10</name>'
assert_contains "${domain}" '<sysbox:kind>windows-image-build</sysbox:kind>'
assert_contains "${domain}" "<source file='${work}/windows.iso'/>"
assert_contains "${domain}" "<source file='${work}/virtio-win.iso'/>"
assert_contains "${domain}" "<source file='${work}/answer.iso'/>"
assert_contains "${domain}" "<source file='${work}/windows10.qcow2'/>"
assert_contains "${domain}" "<target dev='vda' bus='virtio'/>"
assert_not_contains "${domain}" "<boot dev='cdrom'/>"
assert_not_contains "${domain}" "<boot dev='hd'/>"
assert_contains "${domain}" "<interface type='network'>"
assert_contains "${domain}" "<source network='default'/>"

printf 'windows-iso-fixture' >"${work}/windows-source.iso"
printf 'virtio-iso-fixture' >"${work}/virtio-source.iso"
windows_sha="$(sha256sum "${work}/windows-source.iso" | cut -d' ' -f1)"
virtio_sha="$(sha256sum "${work}/virtio-source.iso" | cut -d' ' -f1)"
media_cache="${work}/media-cache"
media_output="${work}/media.out"

SYSBOX_CACHE="${media_cache}" \
WINDOWS_ALLOW_FILE_URL=1 \
WINDOWS_ISO_URL="file://${work}/windows-source.iso" \
WINDOWS_ISO_SHA256="${windows_sha}" \
VIRTIO_ISO_URL="file://${work}/virtio-source.iso" \
VIRTIO_ISO_SHA256="${virtio_sha}" \
  "${root}/scripts/windows/prepare-media.sh" >"${media_output}"

assert_contains "${media_output}" "WINDOWS_ISO=${media_cache}/windows/windows-10-22h2-english-x64.iso"
assert_contains "${media_output}" "VIRTIO_ISO=${media_cache}/windows/virtio-win-0.1.285.iso"
printf '%s  %s\n' "${windows_sha}" "${media_cache}/windows/windows-10-22h2-english-x64.iso" | sha256sum --check --status || fail 'cached Windows ISO checksum mismatch'
printf '%s  %s\n' "${virtio_sha}" "${media_cache}/windows/virtio-win-0.1.285.iso" | sha256sum --check --status || fail 'cached VirtIO ISO checksum mismatch'

rm -f "${work}/windows-source.iso" "${work}/virtio-source.iso"
SYSBOX_CACHE="${media_cache}" \
WINDOWS_ALLOW_FILE_URL=1 \
WINDOWS_ISO_URL="file://${work}/windows-source.iso" \
WINDOWS_ISO_SHA256="${windows_sha}" \
VIRTIO_ISO_URL="file://${work}/virtio-source.iso" \
VIRTIO_ISO_SHA256="${virtio_sha}" \
  "${root}/scripts/windows/prepare-media.sh" >"${work}/cached-media.out"

bad_cache="${work}/bad-cache"
if SYSBOX_CACHE="${bad_cache}" \
  WINDOWS_ALLOW_FILE_URL=1 \
  WINDOWS_ISO_URL="file://${work}/missing.iso" \
  WINDOWS_ISO_SHA256="${windows_sha}" \
  VIRTIO_ISO_URL="file://${media_cache}/windows/virtio-win-0.1.285.iso" \
  VIRTIO_ISO_SHA256="${virtio_sha}" \
  "${root}/scripts/windows/prepare-media.sh" >"${work}/bad.out" 2>"${work}/bad.err"; then
  fail 'missing Windows media source was accepted'
fi
[[ ! -e "${bad_cache}/windows/windows-10-22h2-english-x64.iso" ]] || fail 'failed download left a final Windows ISO'

if SYSBOX_CACHE="${work}/unofficial-cache" \
  WINDOWS_ISO_URL='https://example.com/windows.iso' \
  WINDOWS_ISO_SHA256="${windows_sha}" \
  VIRTIO_ISO_URL="file://${media_cache}/windows/virtio-win-0.1.285.iso" \
  VIRTIO_ISO_SHA256="${virtio_sha}" \
  "${root}/scripts/windows/prepare-media.sh" >"${work}/unofficial.out" 2>"${work}/unofficial.err"; then
  fail 'non-Microsoft Windows media URL was accepted'
fi
assert_contains "${work}/unofficial.err" 'WINDOWS_ISO_URL must use HTTPS and a Microsoft download host'

if SYSBOX_CACHE="${work}/unofficial-virtio-cache" \
  WINDOWS_ISO_URL="file://${media_cache}/windows/windows-10-22h2-english-x64.iso" \
  WINDOWS_ISO_SHA256="${windows_sha}" \
  WINDOWS_ALLOW_FILE_URL=1 \
  VIRTIO_ISO_URL='https://example.com/virtio.iso' \
  VIRTIO_ISO_SHA256="${virtio_sha}" \
  "${root}/scripts/windows/prepare-media.sh" >"${work}/unofficial-virtio.out" 2>"${work}/unofficial-virtio.err"; then
  fail 'non-Fedora VirtIO media URL was accepted'
fi
assert_contains "${work}/unofficial-virtio.err" 'VIRTIO_ISO_URL must use HTTPS and a Fedora Project download host'

fakebin="${work}/fakebin"
mkdir -p "${fakebin}"
cat >"${fakebin}/setfacl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == *'u:libvirt-qemu:rw'* ]]; then
  target="${!#}"
  [[ "$(stat -c '%a' "${target}")" == 600 ]] || {
    printf 'write ACL target must be mode 600: %s\n' "${target}" >&2
    exit 1
  }
fi
printf '%s\n' "$*" >>"${FAKE_ACL_LOG:?}"
EOF
cat >"${fakebin}/qemu-img" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  create) touch "${4}" ;;
  check) [[ -f "$2" ]] ;;
  *) exit 2 ;;
esac
EOF
cat >"${fakebin}/genisoimage" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
while [[ $# -gt 0 ]]; do
  if [[ "$1" == "-output" || "$1" == "-o" ]]; then
    touch "$2"
    exit 0
  fi
  shift
done
exit 2
EOF
cat >"${fakebin}/virsh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
state="${FAKE_VIRSH_STATE:?}"
case "$1" in
  dominfo) [[ "${FAKE_EXISTING_DOMAIN:-0}" == 1 ]] ;;
  define) cp "$2" "${state}.xml"; touch "${state}.defined" ;;
  dumpxml) cat "${state}.xml" ;;
  start) touch "${state}.started" ;;
  domstate) [[ -f "${state}.started" ]] && printf 'shut off\n' || printf 'running\n' ;;
  undefine) rm -f "${state}.defined" ;;
  destroy) : ;;
  *) exit 2 ;;
esac
EOF
cat >"${fakebin}/virt-ls" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
path="${*: -1}"
case "${path}" in
  /Windows/System32)
    [[ "${FAKE_WINDOWS_INSTALLED:-1}" == 1 ]] || exit 1
    printf 'System32\n'
    ;;
  /)
    [[ "${FAKE_BUILD_MARKER:-1}" == 1 ]] || exit 1
    printf 'sysbox-image-build.complete\n'
    ;;
  /Windows)
    if [[ "${FAKE_PANTHER_RESIDUE:-0}" == 1 ]]; then
      printf 'Panther\n'
    fi
    ;;
  *) exit 2 ;;
esac
EOF
cat >"${fakebin}/virt-customize" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${FAKE_SANITIZE_LOG:?}"
EOF
cat >"${fakebin}/virt-win-reg" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == '--merge' ]]; then
  cat "$3" >>"${FAKE_SANITIZE_LOG:?}"
fi
EOF
chmod +x "${fakebin}/qemu-img" "${fakebin}/genisoimage" "${fakebin}/setfacl" "${fakebin}/virsh" "${fakebin}/virt-ls" "${fakebin}/virt-customize" "${fakebin}/virt-win-reg"

build_cache="${work}/build-cache"
mkdir -p "${build_cache}/windows"
cp "${media_cache}/windows/windows-10-22h2-english-x64.iso" "${build_cache}/windows/"
cp "${media_cache}/windows/virtio-win-0.1.285.iso" "${build_cache}/windows/"
build_output="${build_cache}/windows/windows-10-pro-22h2-amd64.qcow2"
FAKE_VIRSH_STATE="${work}/virsh" \
FAKE_ACL_LOG="${work}/acl.log" \
FAKE_SANITIZE_LOG="${work}/sanitize.log" \
PATH="${fakebin}:${PATH}" \
SYSBOX_CACHE="${build_cache}" \
WINDOWS_ADMIN_PASSWORD="${password}" \
WINDOWS_BUILD_TIMEOUT=5 \
WINDOWS_BUILD_POLL_INTERVAL=0 \
  "${root}/scripts/windows/build-windows10.sh" >"${work}/build.out"

[[ -f "${build_output}" ]] || fail 'Windows baseline was not published'
[[ ! -e "${work}/virsh.defined" ]] || fail 'installer domain was not undefined'
assert_contains "${work}/virsh.xml" '<sysbox:kind>windows-image-build</sysbox:kind>'
assert_contains "${work}/build.out" "${build_output}"
assert_contains "${work}/acl.log" 'libvirt-qemu'
assert_contains "${work}/acl.log" 'u:libvirt-qemu:rw'
assert_contains "${work}/sanitize.log" '/Windows/Panther'
assert_contains "${work}/sanitize.log" 'DefaultPassword'

: >"${work}/sanitize.log"
FAKE_VIRSH_STATE="${work}/cache-hit-virsh" \
FAKE_ACL_LOG="${work}/cache-hit-acl.log" \
FAKE_SANITIZE_LOG="${work}/sanitize.log" \
PATH="${fakebin}:${PATH}" \
SYSBOX_CACHE="${build_cache}" \
WINDOWS_ADMIN_PASSWORD="${password}" \
  "${root}/scripts/windows/build-windows10.sh" >"${work}/cache-hit.out"
assert_contains "${work}/sanitize.log" '/Windows/Panther'
assert_contains "${work}/cache-hit.out" "${build_output}"
if rg -l --fixed-strings "${password}" "${build_cache}" >/dev/null 2>&1; then
  fail 'generated cache retained the Windows administrator password'
fi

if FAKE_EXISTING_DOMAIN=1 \
  FAKE_ACL_LOG="${work}/existing-acl.log" \
  FAKE_VIRSH_STATE="${work}/existing-virsh" \
  PATH="${fakebin}:${PATH}" \
  SYSBOX_CACHE="${build_cache}" \
  WINDOWS_ADMIN_PASSWORD="${password}" \
  "${root}/scripts/windows/build-windows10.sh" >"${work}/existing.out" 2>"${work}/existing.err"; then
  fail 'existing installer domain was accepted'
fi
assert_contains "${work}/existing.err" 'domain sysbox-build-windows10 already exists'

incomplete_cache="${work}/incomplete-cache"
mkdir -p "${incomplete_cache}/windows"
cp "${media_cache}/windows/windows-10-22h2-english-x64.iso" "${incomplete_cache}/windows/"
cp "${media_cache}/windows/virtio-win-0.1.285.iso" "${incomplete_cache}/windows/"
if FAKE_WINDOWS_INSTALLED=0 \
  FAKE_VIRSH_STATE="${work}/incomplete-virsh" \
  FAKE_ACL_LOG="${work}/incomplete-acl.log" \
  PATH="${fakebin}:${PATH}" \
  SYSBOX_CACHE="${incomplete_cache}" \
  WINDOWS_ADMIN_PASSWORD="${password}" \
  WINDOWS_BUILD_TIMEOUT=5 \
  WINDOWS_BUILD_POLL_INTERVAL=0 \
  "${root}/scripts/windows/build-windows10.sh" >"${work}/incomplete.out" 2>"${work}/incomplete.err"; then
  fail 'incomplete Windows disk was published'
fi
assert_contains "${work}/incomplete.err" 'Windows installation verification failed'
[[ ! -e "${incomplete_cache}/windows/windows-10-pro-22h2-amd64.qcow2" ]] || fail 'incomplete Windows baseline was published'
[[ ! -e "${incomplete_cache}/windows/windows-10-pro-22h2-amd64.failed.qcow2" ]] || fail 'failed Windows disk was retained without opt-in'

retained_cache="${work}/retained-cache"
mkdir -p "${retained_cache}/windows"
cp "${media_cache}/windows/windows-10-22h2-english-x64.iso" "${retained_cache}/windows/"
cp "${media_cache}/windows/virtio-win-0.1.285.iso" "${retained_cache}/windows/"
: >"${work}/retained-sanitize.log"
if FAKE_WINDOWS_INSTALLED=0 \
  FAKE_VIRSH_STATE="${work}/retained-virsh" \
  FAKE_ACL_LOG="${work}/retained-acl.log" \
  FAKE_SANITIZE_LOG="${work}/retained-sanitize.log" \
  PATH="${fakebin}:${PATH}" \
  SYSBOX_CACHE="${retained_cache}" \
  WINDOWS_ADMIN_PASSWORD="${password}" \
  WINDOWS_KEEP_FAILED_IMAGE=1 \
  WINDOWS_BUILD_TIMEOUT=5 \
  WINDOWS_BUILD_POLL_INTERVAL=0 \
  "${root}/scripts/windows/build-windows10.sh" >"${work}/retained.out" 2>"${work}/retained.err"; then
  fail 'incomplete Windows disk was accepted with diagnostic retention enabled'
fi
retained_image="${retained_cache}/windows/windows-10-pro-22h2-amd64.failed.qcow2"
[[ -e "${retained_image}" ]] || fail 'opt-in failed Windows disk was not retained'
[[ "$(stat -c '%a' "${retained_image}")" == 600 ]] || fail 'retained failed Windows disk must use mode 600'
assert_contains "${work}/retained-sanitize.log" '/Windows/Panther'

marker_cache="${work}/marker-cache"
mkdir -p "${marker_cache}/windows"
cp "${media_cache}/windows/windows-10-22h2-english-x64.iso" "${marker_cache}/windows/"
cp "${media_cache}/windows/virtio-win-0.1.285.iso" "${marker_cache}/windows/"
if FAKE_BUILD_MARKER=0 \
  FAKE_VIRSH_STATE="${work}/marker-virsh" \
  FAKE_ACL_LOG="${work}/marker-acl.log" \
  PATH="${fakebin}:${PATH}" \
  SYSBOX_CACHE="${marker_cache}" \
  WINDOWS_ADMIN_PASSWORD="${password}" \
  WINDOWS_BUILD_TIMEOUT=5 \
  WINDOWS_BUILD_POLL_INTERVAL=0 \
  "${root}/scripts/windows/build-windows10.sh" >"${work}/marker.out" 2>"${work}/marker.err"; then
  fail 'Windows disk without completion marker was published'
fi
assert_contains "${work}/marker.err" 'Windows installation verification failed'

existing_bad_cache="${work}/existing-bad-cache"
mkdir -p "${existing_bad_cache}/windows"
cp "${media_cache}/windows/windows-10-22h2-english-x64.iso" "${existing_bad_cache}/windows/"
cp "${media_cache}/windows/virtio-win-0.1.285.iso" "${existing_bad_cache}/windows/"
touch "${existing_bad_cache}/windows/windows-10-pro-22h2-amd64.qcow2"
if FAKE_WINDOWS_INSTALLED=0 \
  FAKE_VIRSH_STATE="${work}/existing-bad-virsh" \
  FAKE_ACL_LOG="${work}/existing-bad-acl.log" \
  PATH="${fakebin}:${PATH}" \
  SYSBOX_CACHE="${existing_bad_cache}" \
  WINDOWS_ADMIN_PASSWORD="${password}" \
  "${root}/scripts/windows/build-windows10.sh" >"${work}/existing-bad.out" 2>"${work}/existing-bad.err"; then
  fail 'unverified existing Windows baseline was reused'
fi
assert_contains "${work}/existing-bad.err" 'existing Windows baseline failed verification'

windows_topology="${root}/tests/e2e/windows10/field.sysbox.hcl"
windows_e2e="${root}/tests/e2e/windows10_libvirt.sh"
assert_contains "${windows_topology}" 'source       = env("SYSBOX_WINDOWS10_QCOW2")'
assert_contains "${windows_topology}" 'guest_family = "windows"'
assert_contains "${windows_topology}" 'network_init = "preconfigured"'
assert_not_contains "${windows_topology}" 'provisioner '
assert_not_contains "${windows_topology}" 'link '
assert_contains "${windows_e2e}" 'SYSBOX_WINDOWS10_QCOW2'
assert_contains "${windows_e2e}" 'chmod 0755 "${run_dir}"'
assert_contains "${windows_e2e}" 'rm -rf "${run_dir}"'
assert_contains "${windows_e2e}" 'Plan: 0 to add, 0 to replace, 0 to destroy, 2 unchanged.'
assert_contains "${windows_e2e}" 'reset --auto-approve'
assert_contains "${windows_e2e}" 'virsh domuuid'
assert_contains "${root}/Makefile" 'test-windows-tools:'
assert_contains "${root}/Makefile" 'prepare-windows10-media:'
assert_contains "${root}/Makefile" 'build-windows10-image:'
assert_contains "${root}/Makefile" 'test-windows10-libvirt:'
assert_contains "${root}/scripts/windows/build-windows10.sh" 'SUPERMIN_KERNEL'
assert_contains "${root}/scripts/windows/build-windows10.sh" 'SUPERMIN_MODULES'
assert_contains "${root}/scripts/windows/build-windows10.sh" 'sanitize-image.sh'

sanitize_script="${root}/scripts/windows/sanitize-image.sh"
assert_contains "${sanitize_script}" 'AutoAdminLogon'
assert_contains "${sanitize_script}" 'AutoLogonCount'
assert_contains "${sanitize_script}" 'DefaultUserName'
assert_contains "${sanitize_script}" 'DefaultPassword'
assert_contains "${sanitize_script}" "--delete '/Windows/Panther'"
assert_contains "${sanitize_script}" 'virt-win-reg'
assert_contains "${sanitize_script}" 'virt-customize'

residue_image="${work}/residue.qcow2"
touch "${residue_image}"
if FAKE_PANTHER_RESIDUE=1 \
  FAKE_SANITIZE_LOG="${work}/residue-sanitize.log" \
  PATH="${fakebin}:${PATH}" \
  "${sanitize_script}" "${residue_image}" >"${work}/residue.out" 2>"${work}/residue.err"; then
  fail 'sanitization accepted a nested Panther answer file'
fi
assert_contains "${work}/residue.err" 'Windows image sanitization left unattended setup artifacts'

custom_output="${work}/custom/output/windows.qcow2"
FAKE_VIRSH_STATE="${work}/custom-virsh" \
FAKE_ACL_LOG="${work}/custom-acl.log" \
FAKE_SANITIZE_LOG="${work}/custom-sanitize.log" \
PATH="${fakebin}:${PATH}" \
SYSBOX_CACHE="${build_cache}" \
WINDOWS_QCOW2="${custom_output}" \
WINDOWS_ADMIN_PASSWORD="${password}" \
WINDOWS_BUILD_TIMEOUT=5 \
WINDOWS_BUILD_POLL_INTERVAL=0 \
  "${root}/scripts/windows/build-windows10.sh" >"${work}/custom.out"
[[ -f "${custom_output}" ]] || fail 'custom Windows baseline parent was not created'

collision_cache="${work}/collision-cache"
mkdir -p "${collision_cache}/windows"
cp "${media_cache}/windows/windows-10-22h2-english-x64.iso" "${collision_cache}/windows/"
cp "${media_cache}/windows/virtio-win-0.1.285.iso" "${collision_cache}/windows/"
collision_failed="${collision_cache}/windows/windows-10-pro-22h2-amd64.failed.qcow2"
printf 'user-owned' >"${collision_failed}"
if FAKE_VIRSH_STATE="${work}/collision-virsh" \
  FAKE_ACL_LOG="${work}/collision-acl.log" \
  PATH="${fakebin}:${PATH}" \
  SYSBOX_CACHE="${collision_cache}" \
  WINDOWS_ADMIN_PASSWORD="${password}" \
  WINDOWS_KEEP_FAILED_IMAGE=1 \
  "${root}/scripts/windows/build-windows10.sh" >"${work}/collision.out" 2>"${work}/collision.err"; then
  fail 'existing unowned failed artifact was accepted for overwrite'
fi
assert_contains "${work}/collision.err" 'refusing to overwrite existing failed image'
assert_contains "${collision_failed}" 'user-owned'

plan_image="${work}/plan.qcow2"
PATH="${fakebin}:${PATH}" qemu-img create -f qcow2 "${plan_image}" 1G
GOCACHE="${GOCACHE:-/tmp/sysbox-gocache}" go build -buildvcs=false -o "${work}/sysbox" "${root}/cmd/sysbox"
SYSBOX_WINDOWS10_QCOW2="${plan_image}" "${work}/sysbox" --state "${work}/plan-state.json" -f "${windows_topology}" validate >"${work}/validate.out"
SYSBOX_WINDOWS10_QCOW2="${plan_image}" "${work}/sysbox" --state "${work}/plan-state.json" -f "${windows_topology}" plan >"${work}/plan.out"
assert_contains "${work}/validate.out" 'Valid. 1 substrate(s), 2 resource(s)'
assert_contains "${work}/plan.out" 'Plan: 2 to add, 0 to replace, 0 to destroy, 0 unchanged.'

printf 'windows tools tests passed\n'
