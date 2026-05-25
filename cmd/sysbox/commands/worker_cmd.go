package commands

import (
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/api"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/worker"
)

var (
	flagWorkerAPI          string
	flagWorkerID           string
	flagWorkerName         string
	flagWorkerCapabilities string
	flagWorkerConfig       string
	flagWorkerPollInterval time.Duration
)

var workerCmd = &cobra.Command{
	Use:   "worker",
	Short: "Start a legacy sysbox worker agent (use `sysbox agent start`)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadServiceConfig(flagWorkerConfig)
		if err != nil {
			return err
		}
		return worker.Run(cmd.Context(), worker.Options{
			APIURL:       flagWorkerAPI,
			ID:           flagWorkerID,
			Name:         flagWorkerName,
			Capabilities: splitCSV(flagWorkerCapabilities),
			Labels:       map[string]string{"mode": "agent"},
			PollInterval: flagWorkerPollInterval,
		}, api.NewExecutionBridge(cfg))
	},
}

func init() {
	workerCmd.Flags().StringVar(&flagWorkerAPI, "api", "http://127.0.0.1:9876", "sysbox API URL")
	workerCmd.Flags().StringVar(&flagWorkerID, "id", "local", "worker id")
	workerCmd.Flags().StringVar(&flagWorkerName, "name", "", "worker display name")
	workerCmd.Flags().StringVar(&flagWorkerCapabilities, "capabilities", "docker,network,firecracker,kvm,libvirt", "comma-separated worker capabilities")
	workerCmd.Flags().StringVar(&flagWorkerConfig, "config", "", "path to sysbox service YAML config")
	workerCmd.Flags().DurationVar(&flagWorkerPollInterval, "poll-interval", 2*time.Second, "run poll interval")
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
