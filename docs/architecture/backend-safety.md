# Backend Mutation Safety

State backends advertise locking, CAS, snapshots, deletion, leases, and force
unlock separately. Mutation requires both locking and CAS by default. Local,
SQLite, and Postgres backends satisfy this contract; HTTP and S3 do not.

The CLI rejects unsafe apply, destroy, import, and recovery. The explicit
`--allow-unsafe-state` flag overrides the check and must only be used when the
caller guarantees single-writer access.
