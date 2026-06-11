# Firecracker Artifact Preparation

Sysbox does not bake kernels or rootfs images into its own image. Declare them
in HCL as `sysbox_kernel` / `sysbox_image` resources and either mount them
explicitly or let the artifact cache fetch them.

## Kernel

Use an uncompressed `vmlinux` built with vsock and virtio support. The
firecracker-ci kernels work out of the box:

```sh
wget "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.14/x86_64/vmlinux-5.10.245" -O vmlinux
```

## Rootfs

Firecracker boots an ext4 block image. `scripts/prepare-fc-rootfs.sh` builds
one from the firecracker-ci ubuntu-24.04 squashfs (download → unsquash →
mkfs.ext4 → copy). The script is idempotent and caches its output under
`~/.cache/sysbox/rootfs/`.

The rootfs only needs a standard init (or shell) as `chain_init`; everything
sysbox-specific (hostname, SSH keys, env, vsock agent) is injected at boot via
the config drive by `cmd/sysbox-init`. See `cmd/sysbox-init/main.go` for the
boot flow.

## Verification

`scripts/microvm-verify.sh` applies a minimal Firecracker topology end to end
and is the quickest way to confirm kernel + rootfs artifacts are usable.
