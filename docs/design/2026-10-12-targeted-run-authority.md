# Targeted Run authority

Lifecycle Runs already lock and commit the topology aggregate. Their parent
grant therefore owns topology exclusion, including when a Run resets one node.
The immutable Run request still binds target address, starting resource UID,
generation, revision and state serial through its signed digest. Exclusion scope
does not replace effect authorization.

The trusted host checks the starting target against canonical state before
execution. Before signing each child request, it resolves the node through the
same Run's canonical state/checkpoint and checks that it is the selected node.
A reset may use the replacement incarnation from that checkpoint. Different
nodes, topologies and unsupported target resource types fail closed. Topology
Runs may derive children for their own canonical nodes.

Both task and SSH child requests require the enrolled host signature over the
parent and exact child payload/target. The API also validates the issuer, current
parent authority and command lease under the durable authority lock. Child
grants continue to own node scope and must stop before the parent can release.

Run completion reconstructs the immutable target request from the original
command and checks its digest; it requires topology-scoped parent authority.
There is no acceptance fallback for old resource-scoped Run grants or unsigned
derivation requests. Deploy API, Agent and host together on explicitly rebuilt
authority state. Preserve historical unknown journals separately; never rewrite
their identity or infer completion during the upgrade.

This change enables authorization for existing remote targeted reset. It does
not add remote apply/destroy target selection to public admission or implement
automatic SSH service/image enrollment.

Validation: target substitution rejection, scope exclusion and child derivation,
host target checks, receipt replay, then real API/Agent/provider regressions.
