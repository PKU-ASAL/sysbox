package commands

import (
	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/api"
)

var flagServeAddr string

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start HTTP API server",
	Long: `Start the sysbox HTTP API server.

Topology management:
  GET  /v1/topologies
  GET  /v1/topologies/{suite}/state
  GET  /v1/topologies/{suite}/plan
  POST /v1/topologies/{suite}/apply
  POST /v1/topologies/{suite}/destroy

Async run tracking:
  GET  /v1/runs/{id}
  GET  /v1/runs/{id}/logs         (SSE)

Node access:
  GET  /v1/topologies/{suite}/nodes
  GET  /v1/topologies/{suite}/nodes/{node}
  POST /v1/topologies/{suite}/nodes/{node}/exec

Auth: set SYSBOX_API_TOKEN env var to require Bearer token.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		srv := api.NewServer("runs")
		return srv.Start(flagServeAddr)
	},
}

func init() {
	serveCmd.Flags().StringVar(&flagServeAddr, "addr", ":8080", "listen address")
}
