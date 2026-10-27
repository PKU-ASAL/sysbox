# Trusted SSH boot deployment and reboot acceptance

This closes the approved SSH service-deployment slice using the existing
administrator profile and durable installer. Image construction supplies the
helper, issuer public key, enrolled account, explicit final Include and profile.
Image startup invokes installation/recovery automatically. Worker RPCs cannot
select paths, processes, reload commands or administrator configuration.

Systemd guests keep the existing explicit unit adapter. Containers gain a
mutually exclusive `pid_file` adapter: trust the PID file, match the executable
inode, foreground sshd/config arguments and process start time, then signal a
pidfd. OpenSSH's listener process title is recognized; session children and
command-line overrides are rejected. The installer continues to validate the
full staged/effective configuration and preserve administrator-owned files.

The provided guest entrypoint owns the container's sshd child. It removes a stale
regular PID file before launch, waits for the new PID file, invokes the durable
installer and terminates the child on enrollment failure. It never signals a PID
after reaping the child. Package installation is an image-build responsibility;
no distribution detection or runtime package-manager fallback is introduced.

Acceptance uses the existing two-VM API/Agent/host flow and real SSH logins. It
restarts the guest container/service with its journal intact, then reboots both
VM kernels and records changed boot IDs. It explicitly starts the test services
and original owned container after reboot and requires unchanged resource
identity, valid managed/admin login, rejection of the old key and successful
revocation. It then runs receipt-loss recovery, runtime SSH, reset and Destroy.
The deployment profile/main-config hashes must remain unchanged and deployment
must be ready after restart. Historical unknown operations are preserved.

Validation includes parameter substitution/PID-file substitution rejection,
actual OpenSSH pidfd reload, profile conflicts and deployment recovery, full Go
tests, acceptance script tests and the real two-VM gate. This is not automatic
recovery of arbitrary VM workloads or enrollment of arbitrary third-party images.
