# Explicit ownership transfer

Transfer moves an exact stopped provider object between two topology owners on
the same enrolled execution host. It requires a reviewed immutable plan, a
canonical source freeze and a signed host preparation proof. It does not migrate
a VM to another host. An existing unowned object uses adoption instead.

Prepare a version 1 OwnershipTransferPlan with the exact source and target
topology UIDs, addresses, resource UIDs, generations, state serials, owning Agent,
provider external ID and inspection fingerprint. The target generation must
increase and its action ID must equal the plan ID. Obtain these values from
canonical state and actual provider inspection; do not invent missing identity.
The target must have initialized canonical state and both placements must refer
to the same enrolled Agent.

Use the configured authenticated API/proxy and SYSBOX_API_TOKEN. The CLI does
not supply administrator role headers.

```sh
sysbox ownership-transfer prepare plan.json --api https://control.example
```

This reserves the target and freezes the source. A `waiting_for_stop` response
does not mean the source stopped. Wait for canonical active work to stop; if its
outcome is unknown, resolve it through the existing recovery protocol. Preparing
does not retire the source or transfer the provider object.

On the enrolled owning host, obtain its signed inspection:

```sh
sysbox host-fence prepare-transfer plan.json --socket /run/sysbox/host.sock > preparation.json
```

The socket path must match that host's configured supervisor. The host checks the
source freeze and actual stopped provider identity. Submit the unchanged proof,
then explicitly authorize retirement and dispatch:

```sh
sysbox ownership-transfer prepared preparation.json --api https://control.example
sysbox ownership-transfer submit plan.json --api https://control.example
```

The accepted response contains the NodeOperation ID. HTTP 202 is acceptance,
not successful completion. Observe the operation through the existing API;
only a verified host completion publishes target ownership and releases the
source maintenance barrier.

If a response is lost, repeat the same step with the same file. Do not generate
a new plan ID, edit identities or replay the provider effect. Exact submission
retries return the original operation, including after API reconstruction.

Before source retirement, an explicit cancellation is available:

```sh
sysbox ownership-transfer abort plan.json --api https://control.example
```

An `aborted_waiting_for_source_stop` response retains the source barrier until
cessation is proved. Retry the unchanged abort after that proof. After retirement,
abort cannot silently restore the source; unknown effects use evidence-bound
recovery. CLI transport errors never automatically invoke the next step.

Validation boundaries: public-route contracts cover file/SQLite/Postgres,
forged-proof rejection and cold API submission retry. CLI tests cover explicit
steps, bearer forwarding, redirect refusal and invalid plan refusal. These alone
do not establish actual provider effects or production proxy authentication.
