#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${script_dir}/lib.sh"

if [[ $# -ne 5 ]]; then
  windows_die "usage: $0 NAME WINDOWS_ISO VIRTIO_ISO ANSWER_ISO DISK"
  exit 2
fi

name="$(printf '%s' "$1" | windows_xml_escape)"
windows_iso="$(printf '%s' "$2" | windows_xml_escape)"
virtio_iso="$(printf '%s' "$3" | windows_xml_escape)"
answer_iso="$(printf '%s' "$4" | windows_xml_escape)"
disk="$(printf '%s' "$5" | windows_xml_escape)"

cat <<EOF
<domain type='kvm'>
  <name>${name}</name>
  <metadata>
    <sysbox:build xmlns:sysbox='https://github.com/PKU-ASAL/sysbox/windows-build'>
      <sysbox:kind>windows-image-build</sysbox:kind>
    </sysbox:build>
  </metadata>
  <memory unit='MiB'>4096</memory>
  <vcpu>4</vcpu>
  <os>
    <type arch='x86_64' machine='q35'>hvm</type>
  </os>
  <features><acpi/><apic/><pae/></features>
  <cpu mode='host-passthrough'/>
  <clock offset='localtime'/>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2' cache='none' io='native'/>
      <source file='${disk}'/>
      <target dev='vda' bus='virtio'/>
      <boot order='2'/>
    </disk>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='${windows_iso}'/>
      <target dev='sda' bus='sata'/>
      <readonly/>
      <boot order='1'/>
    </disk>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='${virtio_iso}'/>
      <target dev='sdb' bus='sata'/>
      <readonly/>
    </disk>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='${answer_iso}'/>
      <target dev='sdc' bus='sata'/>
      <readonly/>
    </disk>
    <interface type='network'>
      <source network='default'/>
      <model type='virtio'/>
    </interface>
    <graphics type='vnc' autoport='yes' listen='127.0.0.1'/>
    <video><model type='qxl' ram='65536' vram='65536' heads='1'/></video>
    <channel type='unix'>
      <target type='virtio' name='org.qemu.guest_agent.0'/>
    </channel>
  </devices>
  <on_poweroff>destroy</on_poweroff>
  <on_reboot>restart</on_reboot>
  <on_crash>destroy</on_crash>
</domain>
EOF
