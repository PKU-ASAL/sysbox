// Package worker implements the sysbox execution-plane agent.
//
// The API package owns control-plane scheduling, run records, and worker
// registration endpoints. This package owns the agent loop: register,
// heartbeat, poll assigned runs, claim one run, and execute it locally against
// the configured backend.
package worker
