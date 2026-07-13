# Guest Network Initialization Design

## Goal

Make guest network initialization an explicit provider capability, then prove
the contract with a pinned Ubuntu cloud image and a complete
Docker/Firecracker/libvirt IPv4 communication matrix.

## Scope

This batch supports IPv4 only. Public contracts use address-prefix slices so
IPv6 can be added without replacing the interface. The libvirt provider
supports exactly two explicit modes:

- `cloud_init`: generate a per-VM NoCloud seed from declared attachments;
- `preconfigured`: do not mutate guest network configuration and require the
  image to expose the declared addresses itself.

There is no implicit default, automatic detection, fallback, SSH bootstrap, or
permanent compatibility path. Docker and Firecracker retain their existing
deterministic network initialization mechanisms.

## Public Contract

`pkg/substrate` owns the provider-neutral values:

```go
type GuestNetworkInitMode string

const (
    GuestNetworkInitCloudInit     GuestNetworkInitMode = "cloud_init"
    GuestNetworkInitPreconfigured GuestNetworkInitMode = "preconfigured"
)

type GuestNetworkInterfaceObservation struct {
    Name       string
    MAC        string
    IPPrefixes []string
    Converged  bool
    Reason     string
}

type GuestNetworkInitObservation struct {
    Mode       GuestNetworkInitMode
    Converged  bool
    Interfaces []GuestNetworkInterfaceObservation
    Reason     string
}
```

`substrate.Capabilities` advertises `GuestNetworkInitModes`. A provider that
implements an initialization mode must list it. The list is descriptive and is
also enforced by provider validation.

`driver.GuestNetworkInit` is an optional provider capability with two lifecycle
operations:

```go
PrepareGuestNetwork(context.Context, substrate.NodeHandle) error
ObserveGuestNetwork(context.Context, substrate.NodeHandle) (substrate.GuestNetworkInitObservation, error)
```

Preparation belongs to the provider because the required phase differs by
technology. Libvirt creates a seed before domain start. A future agent-based
provider could prepare a config drive or defer work until its agent is ready.
Runtime only requires the capability, records the observation, and rejects a
non-converged result.

## Configuration

Every libvirt node must select one mode explicitly:

```hcl
provider "libvirt" {
  network_init      = "cloud_init"
  ssh_user          = "sysbox"
  ssh_key           = "/run/secrets/sysbox_matrix_key"
  ssh_authorized_key = env("SYSBOX_MATRIX_SSH_PUBLIC_KEY")
}
```

`network_init` accepts only `cloud_init` and `preconfigured`. Missing and
unknown values fail decode/validation before mutation. SSH user and authorized
key injection are optional parts of the libvirt cloud-init seed; the acceptance
fixture requires them so reverse-edge communication is observable. Existing
libvirt examples are updated explicitly; there is no legacy default.

The typed libvirt `Config` carries `NetworkInit` and
`SSHAuthorizedKey`. Execution-scoped secret resolution preserves the concrete
typed config while resolving its string fields. The chosen mode is copied to
the provider-owned handle state so recovery and destroy never depend on
re-decoding current configuration. Public-key material is used to build the
ephemeral seed and is not copied into state.

## Lifecycle

Node attachment remains cold-plug for libvirt:

1. Runtime creates the provider node handle.
2. Runtime passes normalized attachments containing logical name, stable MAC,
   IPv4 prefixes, gateway, isolated namespace, and libvirt-visible root bridge.
3. Runtime calls `PrepareGuestNetwork` before `StartNode`.
4. In `cloud_init`, libvirt creates a NoCloud seed with metadata, cloud-config
   user data creating the declared SSH user and authorized key, and v2 network
   config matched by stable MAC. In `preconfigured`, preparation is an explicit
   no-op.
5. Libvirt defines and starts the domain.
6. Runtime calls `ObserveGuestNetwork` until the provider returns converged or
   the bounded readiness deadline expires.
7. Runtime stores the final observation with the node resource.

The seed ISO is a provider-owned runtime artifact under the VM directory. It
is read-only in domain XML and removed with the VM directory during destroy or
checkpoint cleanup.

## Convergence Verification

Libvirt verifies every declared IPv4 address from the isolated network
namespace. The provider uses the attachment's persisted namespace and strips
the prefix before probing. A successful observation requires all declared
interfaces to respond before the readiness deadline.

The probe is bounded and cancellation-aware. Timeout errors identify the
logical attachment, MAC, expected address, mode, and last probe failure. Empty
attachments converge immediately. IPv6 prefixes produce an explicit
unsupported-family error in this batch rather than being silently ignored.

`preconfigured` uses the same observation path as `cloud_init`; its contract is
therefore testable rather than a promise that the provider cannot verify.

## State And Recovery

State persists:

- the selected initialization mode in provider-private handle state;
- normalized attachment intent already owned by the node resource;
- the final provider-neutral observation.

State does not persist seed contents, environment values, SSH credentials, or
download tokens. Checkpoint recovery rebuilds the typed handle and observes the
same declared addresses. Cleanup remains provider-owned and idempotent.

Changing `network_init` changes the desired hash and replaces the node. There
is no in-place transition between initialization modes.

## Controlled Image

The acceptance image is an official Ubuntu 24.04 LTS server cloud image for
amd64. The repository includes a preparation script with an immutable release
URL and SHA256. The script downloads into the configured Sysbox cache, verifies the
digest before installation, and reuses a matching cached file. It never edits
the upstream image.

The exact release URL and digest are selected from Ubuntu's official checksum
manifest during implementation and checked into the script and verification
document. A digest mismatch is fatal. Offline runs succeed from the verified
cache and otherwise fail with a command that prepares the artifact.

## Acceptance Matrix

`examples/heterogeneous-matrix` uses:

- Docker at `10.44.0.10/24`;
- Firecracker at `10.44.0.20/24`;
- libvirt with `network_init = "cloud_init"` at `10.44.0.30/24`.

The privileged acceptance runner must prove:

1. eight resources apply successfully;
2. each node owns its declared IPv4 address;
3. Docker can reach Firecracker and libvirt;
4. Firecracker can reach Docker and libvirt;
5. libvirt can reach Docker and Firecracker;
6. a repeated plan reports eight unchanged resources;
7. destroy succeeds;
8. no owned container, domain, namespace, bridge, veth, tap, VM process,
   temporary VM directory, or seed ISO remains.

The runner uses existing provider execution channels: Docker exec,
Firecracker vsock/SSH as available, and libvirt SSH from the cloud image's
NoCloud-authorized key. The runner generates an Ed25519 keypair per run, passes
the public key through an execution-scoped environment secret, mounts the
private key read-only, and removes both during cleanup. Test credentials are
never committed or persisted in state.

## Error Handling

- Configuration errors fail validate/plan before mutation.
- Seed generation failure aborts before domain definition.
- Domain start failure undefines the managed domain and removes its VM
  directory.
- Convergence failure destroys the partially-created managed domain through
  the existing checkpoint cleanup path.
- Cleanup errors retain ownership identity and report exact residue; they do
  not delete similarly named unmanaged resources.

## Testing

Unit tests cover mode parsing, mandatory explicit configuration, capability
advertisement, desired-hash replacement, NoCloud output, preconfigured no-op,
IPv4 observation, IPv6 rejection, observation persistence, and recovery.

Privileged tests cover root bridge transit, real namespace probes, Docker
named-netns preservation, libvirt domain/seed lifecycle, all six directed
guest communication edges, repeated plan, destroy, and residue audit.

Standard gates remain `go test ./...`, `go vet ./...`, focused race,
`make ci`, `make test-privileged-container`, removal audit, and
`git diff --check`.
