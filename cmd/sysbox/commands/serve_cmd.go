package commands

import (
	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/api"
	"github.com/oslab/sysbox/pkg/config"
)

var (
	flagServeAddr          string
	flagServeRunsDir       string
	flagServeWorkspacesDir string
	flagServeConfig        string
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
  DELETE /v1/topologies/{topology}                 (delete empty topology metadata)

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

Configuration:
  sysbox serve --config /etc/sysbox/sysbox.yaml

Environment variables remain deployment overrides for API listen/token,
state backend, mounted paths, supervisor policy, and provider binaries.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadServiceConfig(flagServeConfig)
		if err != nil {
			return err
		}
		if cmd.Flags().Changed("addr") {
			cfg.API.Listen = flagServeAddr
		}
		if cmd.Flags().Changed("runs") {
			cfg.Paths.RunsDir = flagServeRunsDir
		}
		if cmd.Flags().Changed("workspaces") {
			cfg.Paths.WorkspacesDir = flagServeWorkspacesDir
		}
		srv := api.NewServerWithConfig(cfg)
		return srv.Start(cfg.API.Listen)
	},
}

func init() {
	serveCmd.Flags().StringVar(&flagServeAddr, "addr", ":9876", "listen address")
	serveCmd.Flags().StringVar(&flagServeRunsDir, "runs", config.DefaultRunsDir(), "directory for local run/checkpoint files")
	serveCmd.Flags().StringVar(&flagServeWorkspacesDir, "workspaces", config.DefaultWorkspacesDir(), "directory containing per-topology HCL workspaces")
	serveCmd.Flags().StringVar(&flagServeConfig, "config", "", "path to sysbox service YAML config")
}
