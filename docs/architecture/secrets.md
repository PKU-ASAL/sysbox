# Secret References

`env("NAME")` produces `secret://env/NAME`; it does not read plaintext during
configuration evaluation. Values are resolved immediately before provider
operations. Missing values fail execution. Non-sensitive optional path lookup
uses `env_optional("NAME")`.

Plans, state, checkpoints, API payloads, and logs store only references or
redacted placeholders. Node and actor environment, commands, connection
credentials, provisioner commands, authorized keys, and provider config pass
through the execution-scoped resolver.
