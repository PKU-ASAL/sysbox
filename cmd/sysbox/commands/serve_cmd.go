package commands

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/api"
)

var (
	flagServeAddr          string
	flagServeRunsDir       string
	flagServeWorkspacesDir string
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start HTTP API server",
	Long: `Start the sysbox HTTP API server.

Topology CRUD:
  POST   /v1/topologies                           (create: upload HCL)
  GET    /v1/topologies                            (list topologies)
  GET    /v1/topologies/{topology}                 (topology metadata)
  GET    /v1/topologies/{topology}/hcl             (read HCL source)
  PUT    /v1/topologies/{topology}/hcl             (update HCL source)
  DELETE /v1/topologies/{topology}                 (auto-destroy + delete)

Topology operations:
  GET  /v1/topologies/{topology}/state
  GET  /v1/topologies/{topology}/plan
  GET  /v1/topologies/{topology}/graph             (visualization nodes+edges)
  POST /v1/topologies/{topology}/apply
  POST /v1/topologies/{topology}/destroy

Async run tracking:
  GET  /v1/runs/{id}
  GET  /v1/runs/{id}/logs                         (SSE)

Node access:
  GET  /v1/topologies/{topology}/nodes
  GET  /v1/topologies/{topology}/nodes/{node}
  POST /v1/topologies/{topology}/nodes/{node}/exec

Environment overrides:
  SYSBOX_API_LISTEN       listen address (default :9876)
  SYSBOX_RUNS_DIR         state dir       (default runs)
  SYSBOX_WORKSPACES_DIR   HCL dir         (default examples)
  SYSBOX_CACHE_DIR        artifact cache root
  SYSBOX_TOOLS_DIR        mounted tools dir
  SYSBOX_API_TOKEN        require Bearer token when non-empty`,
	RunE: func(cmd *cobra.Command, args []string) error {
		addr := envOr("SYSBOX_API_LISTEN", flagServeAddr)
		runs := envOr("SYSBOX_RUNS_DIR", flagServeRunsDir)
		workspaces := envOr("SYSBOX_WORKSPACES_DIR", flagServeWorkspacesDir)
		srv := api.NewServer(runs, workspaces)
		return srv.Start(addr)
	},
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func init() {
	serveCmd.Flags().StringVar(&flagServeAddr, "addr", ":9876", "listen address")
	serveCmd.Flags().StringVar(&flagServeRunsDir, "runs", "runs", "directory for state.json files")
	serveCmd.Flags().StringVar(&flagServeWorkspacesDir, "workspaces", "examples", "directory containing per-topology HCL workspaces")
}
