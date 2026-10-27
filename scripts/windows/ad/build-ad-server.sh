#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
media_dir="${WINDOWS_MEDIA_DIR:-/home/ybh/cyberfield-vm/downloads/windows}"
windows_iso="${WINDOWS_SERVER_ISO:-${media_dir}/windows-server-2025-eval-en-us.iso}"
virtio_iso="${VIRTIO_ISO:-${media_dir}/virtio-win-0.1.285.iso}"
output="${WINDOWS_AD_QCOW2:-/var/tmp/cyberfield-windows-ad/windows-ad-server-2025.qcow2}"
domain="${WINDOWS_AD_BUILD_DOMAIN:-sysbox-build-windows-ad}"
timeout="${WINDOWS_AD_BUILD_TIMEOUT:-14400}"

[[ "$EUID" == 0 ]] || { echo 'run this image builder as root (sudo)' >&2; exit 1; }
for tool in genisoimage qemu-img virsh setfacl qemu-nbd ntfs-3g modprobe blockdev python3 openssl; do
  command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 1; }
done
[[ -f "$windows_iso" && -f "$virtio_iso" ]] || { echo 'Windows Server and VirtIO ISOs are required' >&2; exit 1; }
[[ "$domain" =~ ^sysbox-build-[a-z0-9-]+$ ]] || { echo 'invalid domain name' >&2; exit 1; }
[[ ! -e "$output" ]] || { echo "existing image: $output" >&2; exit 1; }
virsh -c qemu:///system dominfo "$domain" >/dev/null 2>&1 && { echo "domain exists: $domain" >&2; exit 1; }

mkdir -p "$(dirname "$output")"
chmod 0700 "$(dirname "$output")"
build_dir="$(mktemp -d /var/tmp/sysbox-windows-ad-build.XXXXXX)"
chmod 0711 "$build_dir"
defined=0
success=0
cleanup() {
  if [[ "$defined" == 1 ]]; then
    virsh -c qemu:///system destroy "$domain" >/dev/null 2>&1 || true
    virsh -c qemu:///system undefine "$domain" >/dev/null 2>&1 || true
  fi
  if [[ "$success" == 1 || "${WINDOWS_AD_KEEP_FAILED:-0}" != 1 ]]; then
    rm -rf "$build_dir"
  else
    echo "failed build preserved at $build_dir" >&2
  fi
}
trap cleanup EXIT

admin_password="$(openssl rand -hex 16)Aa!"
dsrm_password="$(openssl rand -hex 16)Bb!"
credential_file="$(dirname "$output")/.operator-credentials"
( umask 077; printf 'Administrator=%s\nDSRM=%s\n' "$admin_password" "$dsrm_password" > "$credential_file" )
export BUILD_DIR="$build_dir" ADMIN_PASSWORD="$admin_password" DSRM_PASSWORD="$dsrm_password" SCRIPT_DIR="$script_dir"
python3 - <<'PY'
import os
from pathlib import Path
source = Path(os.environ['SCRIPT_DIR'])
target = Path(os.environ['BUILD_DIR'])
(target / 'Autounattend.xml').write_text(
    (source / 'Autounattend.xml.tmpl').read_text().replace('__ADMIN_PASSWORD__', os.environ['ADMIN_PASSWORD']),
    encoding='utf-8',
)
(target / 'bootstrap.ps1').write_text(
    (source / 'bootstrap.ps1').read_text().replace('__DSRM_PASSWORD__', os.environ['DSRM_PASSWORD']),
    encoding='utf-8',
)
for name in ('finish.ps1', 'set-flag.ps1'):
    (target / name).write_bytes((source / name).read_bytes())
PY
unset admin_password dsrm_password ADMIN_PASSWORD DSRM_PASSWORD

(
  cd "$build_dir"
  genisoimage -quiet -output answer.iso -volid SYSCFG -joliet -rock Autounattend.xml bootstrap.ps1 finish.ps1 set-flag.ps1
)
rm -f "$build_dir/Autounattend.xml" "$build_dir/bootstrap.ps1" "$build_dir/finish.ps1" "$build_dir/set-flag.ps1"
cp "$windows_iso" "$build_dir/windows.iso"
cp "$virtio_iso" "$build_dir/virtio.iso"
qemu-img create -f qcow2 "$build_dir/disk.qcow2" 64G >/dev/null
chmod 0644 "$build_dir/windows.iso" "$build_dir/virtio.iso" "$build_dir/answer.iso"
chmod 0600 "$build_dir/disk.qcow2"
setfacl -m u:libvirt-qemu:rw "$build_dir/disk.qcow2"
for image in windows.iso virtio.iso answer.iso; do setfacl -m u:libvirt-qemu:r "$build_dir/$image"; done

cat > "$build_dir/domain.xml" <<EOF
<domain type='kvm'>
  <name>${domain}</name>
  <metadata><sysbox:build xmlns:sysbox='https://github.com/PKU-ASAL/sysbox/windows-build'><sysbox:kind>windows-ad-image-build</sysbox:kind></sysbox:build></metadata>
  <memory unit='MiB'>6144</memory><vcpu>4</vcpu>
  <os><type arch='x86_64' machine='q35'>hvm</type></os>
  <features><acpi/><apic/><pae/></features><cpu mode='host-passthrough'/><clock offset='localtime'/>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type='file' device='disk'><driver name='qemu' type='qcow2'/><source file='${build_dir}/disk.qcow2'/><target dev='sda' bus='sata'/><boot order='2'/></disk>
    <disk type='file' device='cdrom'><driver name='qemu' type='raw'/><source file='${build_dir}/windows.iso'/><target dev='sdb' bus='sata'/><readonly/><boot order='1'/></disk>
    <disk type='file' device='cdrom'><driver name='qemu' type='raw'/><source file='${build_dir}/virtio.iso'/><target dev='sdc' bus='sata'/><readonly/></disk>
    <disk type='file' device='cdrom'><driver name='qemu' type='raw'/><source file='${build_dir}/answer.iso'/><target dev='sdd' bus='sata'/><readonly/></disk>
    <interface type='network'><source network='default'/><model type='e1000e'/></interface>
    <graphics type='vnc' autoport='yes' listen='127.0.0.1'/><video><model type='vga'/></video>
    <channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0'/></channel>
  </devices>
  <on_poweroff>destroy</on_poweroff><on_reboot>restart</on_reboot><on_crash>destroy</on_crash>
</domain>
EOF
virsh -c qemu:///system define "$build_dir/domain.xml" >/dev/null
defined=1
virsh -c qemu:///system start "$domain" >/dev/null
echo "Windows Server installation started: $domain"

deadline=$(( $(date +%s) + timeout ))
while :; do
  state="$(virsh -c qemu:///system domstate "$domain" | tr '[:upper:]' '[:lower:]' | xargs)"
  [[ "$state" == 'shut off' ]] && break
  if (( $(date +%s) >= deadline )); then
    echo "installation timed out; inspect: virsh vncdisplay $domain" >&2
    exit 1
  fi
  sleep 15
done
if ! qemu-img check "$build_dir/disk.qcow2" >/dev/null; then
  echo 'Windows disk failed qemu-img check' >&2; exit 1
fi
verify_marker() {
  local nbd='' candidate mount_dir="$build_dir/verify" found=0 mounted=0
  modprobe nbd max_part=8
  for candidate in /dev/nbd{0..15}; do
    [[ -b "$candidate" ]] || continue
    [[ "$(blockdev --getsize64 "$candidate")" == 0 ]] || continue
    if qemu-nbd --read-only --format=qcow2 --connect="$candidate" "$build_dir/disk.qcow2"; then
      nbd="$candidate"
      break
    fi
  done
  [[ -n "$nbd" ]] || { echo 'no free NBD device for Windows marker verification' >&2; return 1; }
  for _ in {1..20}; do
    [[ -b "${nbd}p1" ]] && break
    sleep 1
  done
  mkdir -p "$mount_dir"
  if ntfs-3g -o ro "${nbd}p1" "$mount_dir"; then
    mounted=1
    [[ -f "$mount_dir/sysbox-image-build.complete" ]] && found=1
  fi
  if [[ "$mounted" == 1 ]]; then umount "$mount_dir"; fi
  qemu-nbd --disconnect "$nbd"
  [[ "$found" == 1 ]]
}
if ! verify_marker; then
  echo 'Windows AD setup did not write completion marker' >&2; exit 1
fi
temporary="${output}.tmp"
[[ ! -e "$temporary" ]] || { echo "temporary output exists: $temporary" >&2; exit 1; }
qemu-img convert -f qcow2 -O qcow2 -c "$build_dir/disk.qcow2" "$temporary"
chmod 0644 "$temporary"
mv "$temporary" "$output"
success=1
echo "$output"
