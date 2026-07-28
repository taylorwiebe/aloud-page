package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/taylorwiebe/planreader/internal/agentbridge"
)

func newBridgeCommand(stdout io.Writer) *cobra.Command {
	return newBridgeCommandWithClient(stdout, http.DefaultClient)
}

func newBridgeCommandWithClient(stdout io.Writer, client *http.Client) *cobra.Command {
	command := &cobra.Command{
		Use:    "bridge",
		Short:  "Connect the current agent task to a reader",
		Hidden: true,
	}
	command.AddCommand(newBridgeProbeCommand(stdout))
	command.AddCommand(newBridgeWaitCommand(stdout, client), newBridgePublishCommand(stdout, client), newBridgeDecisionCommand(stdout, client))
	return command
}

func newBridgeDecisionCommand(stdout io.Writer, client *http.Client) *cobra.Command {
	var endpoint, secret, actionID string
	command := &cobra.Command{
		Use:   "decision",
		Short: "Wait for the reader's decision on one proposal",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			values := url.Values{}
			values.Set("action_id", actionID)
			return bridgeRequest(command.Context(), client, http.MethodGet, endpoint+"/task/decisions?"+values.Encode(), secret, nil, stdout)
		},
	}
	command.Flags().StringVar(&endpoint, "endpoint", "", "private reader bridge endpoint")
	command.Flags().StringVar(&secret, "secret", "", "private task secret")
	command.Flags().StringVar(&actionID, "action", "", "proposal action identity")
	_ = command.MarkFlagRequired("endpoint")
	_ = command.MarkFlagRequired("secret")
	_ = command.MarkFlagRequired("action")
	return command
}

func newBridgeWaitCommand(stdout io.Writer, client *http.Client) *cobra.Command {
	var endpoint, secret, attachmentID string
	command := &cobra.Command{
		Use:   "wait",
		Short: "Wait for a turn from the attached reader",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			values := url.Values{}
			values.Set("attachment_id", attachmentID)
			return bridgeRequest(command.Context(), client, http.MethodGet, endpoint+"/task/turns/wait?"+values.Encode(), secret, nil, stdout)
		},
	}
	command.Flags().StringVar(&endpoint, "endpoint", "", "private reader bridge endpoint")
	command.Flags().StringVar(&secret, "secret", "", "private task secret")
	command.Flags().StringVar(&attachmentID, "attachment", "", "exact attachment identity")
	_ = command.MarkFlagRequired("endpoint")
	_ = command.MarkFlagRequired("secret")
	_ = command.MarkFlagRequired("attachment")
	return command
}

func newBridgePublishCommand(stdout io.Writer, client *http.Client) *cobra.Command {
	var endpoint, secret string
	command := &cobra.Command{
		Use:   "publish",
		Short: "Publish one normalized event to the attached reader",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			body, err := io.ReadAll(io.LimitReader(command.InOrStdin(), (64<<10)+1))
			if err != nil {
				return err
			}
			if len(body) > 64<<10 {
				return errors.New("event exceeds 64 KiB")
			}
			return bridgeRequest(command.Context(), client, http.MethodPost, endpoint+"/task/events", secret, body, stdout)
		},
	}
	command.Flags().StringVar(&endpoint, "endpoint", "", "private reader bridge endpoint")
	command.Flags().StringVar(&secret, "secret", "", "private task secret")
	_ = command.MarkFlagRequired("endpoint")
	_ = command.MarkFlagRequired("secret")
	return command
}

func bridgeRequest(ctx context.Context, client *http.Client, method, endpoint, secret string, body []byte, stdout io.Writer) error {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("X-Planreader-Task-Secret", secret)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		return fmt.Errorf("bridge returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	_, err = io.Copy(stdout, response.Body)
	return err
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
