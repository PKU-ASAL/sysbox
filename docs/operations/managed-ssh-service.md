# Managed OpenSSH service enrollment

Sysbox manages a dedicated key lookup file. It preserves the administrator's
main sshd configuration and authorized_keys files. OpenSSH must already be
installed; Sysbox does not choose a package manager or enable password login.

The current supported layout uses one explicit final Include, with no additional
Includes, and a Sysbox file beside the main configuration:

```text
Include /etc/ssh/sysbox-managed.conf
```

Enroll this layout deliberately. Do not append it blindly to a configuration
with conditional Match sections. The installer preserves Include semantics and
checks the complete config with `sshd -t` and `sshd -T -C`. Existing third-party
AuthorizedKeysCommand directives, unsupported layouts and foreign drop-in files
are rejected. There is no automatic migration of an existing unowned drop-in.

Install a root-owned, non-writable-by-others profile, for example
`/etc/sysbox/sshd-service.json`:

```json
{
  "version": 1,
  "root": "/var/lib/sysbox/ssh-service",
  "config_path": "/etc/ssh/sshd_config",
  "drop_in_path": "/etc/ssh/sysbox-managed.conf",
  "helper": "/usr/local/bin/sysbox-guest-task",
  "command_user": "root",
  "sshd": "/usr/sbin/sshd",
  "systemctl": "/usr/bin/systemctl",
  "service_unit": "ssh.service",
  "listeners": ["127.0.0.1:22"],
  "probes": [{
    "User": "ubuntu",
    "RemoteHost": "client.example",
    "RemoteAddress": "192.0.2.10",
    "LocalAddress": "127.0.0.1",
    "LocalPort": 22,
    "AuthorizedKeysFile": ".ssh/authorized_keys .ssh/authorized_keys2"
  }]
}
```

Replace service name, endpoints, user and key-file setting with the actual
deployment. Include every relevant user/source/endpoint class in the probes,
including unmanaged administrator access. Finite probes do not prove arbitrary
Match rules equivalent. The specified service must use `config_path` as its main
config. Executable paths must be absolute, trusted and free of symlink components.
Create the private journal's parent directory before installation.

Run the current guest helper as the service administrator:

```sh
/usr/local/bin/sysbox-guest-task --ssh-service-install /etc/sysbox/sshd-service.json
/usr/local/bin/sysbox-guest-task --ssh-service-recover /etc/sysbox/sshd-service.json
```

Installation stages and validates the whole configuration, durably records
intent, atomically installs the drop-in, revalidates the installed configuration,
reloads the enrolled systemd unit and waits up to five seconds for readiness.
Recovery reuses the recorded bytes and service identity. It refuses changed
profiles, administrator edits and damaged journals. A failed reload remains
pending; successful file publication alone is not recorded as ready.

Enroll and install the service before using managed access keys. The helper's
existing issuer trust file and guest key-record store remain required. Automatic
image enrollment is not implemented by these commands. Local CLI signing and
bootstrap are described in [local-ssh-authority.md](local-ssh-authority.md).
Runtime SSH resources now require schema 2 state; old path-based state
must be explicitly rebuilt and is never silently imported.

`scripts/acceptance/ssh_service.py` validates the real systemd/helper deployment
across the retained two-VM lab, using a separate test service and port. It retains
evidence and stops/removes the test unit. This test checks administrator login
preservation; it does not claim the full API/Agent managed-key lifecycle.
