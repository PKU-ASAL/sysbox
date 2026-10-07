# P9 real acceptance

`make test-p9-real` is the explicit real environment gate for the current
distributed runtime slice. It runs the API/Agent completion-loss test, guest
task supervisor recovery, Docker pause/resume fencing, signed file installation,
Docker adoption cold lifecycle, Firecracker/KVM lifecycle and libvirt generation
fencing.

The target intentionally fails when Docker, KVM, the Firecracker artifacts or
libvirt are unavailable. A skipped individual test is not a P9 pass. Record the
commit, kernel/rootfs/firecracker paths, Docker image digests, libvirt URI and
cleanup results with the release evidence.

This matrix proves the currently migrated guest task, file, node power and
adoption boundaries. Ordinary multi-resource Run execution, full sshd service
management, automatic GC deletion, and distinct-host API/Agent acceptance
remain separate gates until their provider and cross-host contracts are wired.

2026-10-08 real run: signed file installation, Docker task supervisor, and
Docker node power scenarios passed in the available Docker/KVM environment.
The matrix still fails at `TestRemoteDockerCompletionLossRecoveryE2E`: the
legacy in-process Agent fixture reaches Docker image entry without a host-issued
guest task grant. This is a genuine P2/P9 failure, not a skipped test.
