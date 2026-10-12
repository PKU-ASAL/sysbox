# Local CLI managed SSH

Local `apply`, `apply --target` and `destroy` use the same persistent SSH
admission, signed grants and host completion checks as remote execution. A local
controller runs inside the CLI; it opens no HTTP listener and is not advertised
as an online Agent. A later CLI process recovers pending host receipts before
starting new effects. Existing old path-based SSH state still requires rebuild.

Initialize a private authority explicitly:

```sh
sysbox init --authority-dir .sysbox/authority
export SYSBOX_CONFIG="$PWD/.sysbox/authority/sysbox.json"
```

This publishes issuer and host keys, the issuer public key and service config
atomically. All files are private. Existing directories are rejected, including
empty directories and symlinks; initialization never rotates installed guest
trust. The command prints paths, not key material or the generated API token.
The generated service configuration binds to loopback if separately served.

Use the same configuration for subsequent CLI invocations. Its `paths.runs_dir`
owns authority metadata and session journals. `--state` / `--backend` select the
canonical lifecycle store. SQL authority uses that configured database; file
authority retains the configured metadata directory. Do not create another
authority directory to bypass an unresolved operation on the same topology.

Guests must already have the current helper and the matching `issuer.pub` at
`/etc/sysbox/grant-public-key`. Docker can mount those files read-only; Firecracker
uses the configured guest trust key. Enroll the SSH service as described in
[managed-ssh-service.md](managed-ssh-service.md). Automatic image enrollment is
a separate unfinished capability.

The node must declare its canonical guest account, for example:

```hcl
connection {
  type = "docker"
  user = "root"
}
```

Then manage `sysbox_ssh_access` using normal apply/destroy commands. Rotation
uses the persisted revision; stale deletion cannot remove a newer configuration.
Destroy reads the node and connection from state, so the original HCL file need
not exist. Missing/mismatched signing keys fail during preparation before runtime
provider calls. A topology assigned to another execution host is rejected.

Validated with actual independent CLI processes and Docker on file and SQLite
backends: initialize → create → targeted key replacement → destroy without HCL.
Separate tests lose host completion after guest commit and reconstruct the local
controller, verifying exact receipt replay, command retirement and authority
release. Local PostgreSQL CLI and remote resource-targeted Run acceptance are
not claimed by these tests.
