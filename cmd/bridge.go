package cmd

import (
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/taylorwiebe/planreader/internal/agentbridge"
)

func newBridgeCommand(stdout io.Writer) *cobra.Command {
	command := &cobra.Command{
		Use:    "bridge",
		Short:  "Connect the current agent task to a reader",
		Hidden: true,
	}
	command.AddCommand(newBridgeProbeCommand(stdout))
	return command
}

func newBridgeProbeCommand(stdout io.Writer) *cobra.Command {
	var provider string
	command := &cobra.Command{
		Use:   "probe",
		Short: "Report whether the current task can attach safely",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			environment := make(map[string]string)
			for _, item := range os.Environ() {
				key, value, ok := strings.Cut(item, "=")
				if ok {
					environment[key] = value
				}
			}
			return json.NewEncoder(stdout).Encode(agentbridge.Probe(provider, environment))
		},
	}
	command.Flags().StringVar(&provider, "provider", "", "agent provider to inspect")
	_ = command.MarkFlagRequired("provider")
	return command
}
