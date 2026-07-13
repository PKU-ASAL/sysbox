# Heterogeneous Matrix

This acceptance topology places Docker, Firecracker, and libvirt nodes on one
declared isolated IPv4 network. It requires local Firecracker kernel/rootfs and
Firecracker kernel/rootfs paths through `SYSBOX_KERNEL` and `SYSBOX_ROOTFS`.
The acceptance runner prepares a digest-pinned Ubuntu 24.04 cloud image for
libvirt and generates an ephemeral SSH key for the NoCloud seed.

Run `make test-heterogeneous-matrix`. The privileged runner proves all six
directed communication edges, checks a repeated plan is unchanged, destroys
the topology, and audits owned residue.
