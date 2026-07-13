# Heterogeneous Matrix

This acceptance topology places Docker, Firecracker, and libvirt nodes on one
declared isolated IPv4 network. It requires local Firecracker kernel/rootfs and
libvirt qcow2 paths through `SYSBOX_KERNEL`, `SYSBOX_ROOTFS`, and
`SYSBOX_QCOW2`.

The privileged acceptance runner applies the topology, verifies the Docker node
can reach the Firecracker and libvirt addresses, checks a repeated plan is
unchanged, destroys the topology, and audits owned residue.
