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
	"path/filepath"
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
	command.AddCommand(newBridgeWaitCommand(stdout, client), newBridgePublishCommand(stdout, client), newBridgeDecisionCommand(stdout, client), newBridgeReconcileCommand(stdout, client))
	return command
}

func newBridgeReconcileCommand(stdout io.Writer, client *http.Client) *cobra.Command {
	var descriptorPath, actionID, digest, revision string
	command := &cobra.Command{
		Use: "reconcile", Short: "Reconcile an approved source change",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			descriptor, err := readBridgeDescriptor(descriptorPath)
			if err != nil {
				return err
			}
			body, err := json.Marshal(agentbridge.ReconcileRequest{
				ActionID: actionID, ProposalDigest: digest, DocumentRevision: revision,
			})
			if err != nil {
				return err
			}
			return bridgeRequest(command.Context(), client, http.MethodPost, descriptor.Endpoint+"/task/reconcile", descriptor.TaskSecret, body, stdout)
		},
	}
	command.Flags().StringVar(&descriptorPath, "descriptor", "", "owner-only reader bridge descriptor")
	command.Flags().StringVar(&actionID, "action", "", "approved proposal action identity")
	command.Flags().StringVar(&digest, "proposal-digest", "", "approved proposal digest")
	command.Flags().StringVar(&revision, "document-revision", "", "approved document revision")
	_ = command.MarkFlagRequired("descriptor")
	_ = command.MarkFlagRequired("action")
	_ = command.MarkFlagRequired("proposal-digest")
	_ = command.MarkFlagRequired("document-revision")
	return command
}

func newBridgeDecisionCommand(stdout io.Writer, client *http.Client) *cobra.Command {
	var descriptorPath, actionID string
	command := &cobra.Command{
		Use:   "decision",
		Short: "Wait for the reader's decision on one proposal",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			descriptor, err := readBridgeDescriptor(descriptorPath)
			if err != nil {
				return err
			}
			values := url.Values{}
			values.Set("action_id", actionID)
			return bridgeRequest(command.Context(), client, http.MethodGet, descriptor.Endpoint+"/task/decisions?"+values.Encode(), descriptor.TaskSecret, nil, stdout)
		},
	}
	command.Flags().StringVar(&descriptorPath, "descriptor", "", "owner-only reader bridge descriptor")
	command.Flags().StringVar(&actionID, "action", "", "proposal action identity")
	_ = command.MarkFlagRequired("descriptor")
	_ = command.MarkFlagRequired("action")
	return command
}

func newBridgeWaitCommand(stdout io.Writer, client *http.Client) *cobra.Command {
	var descriptorPath string
	command := &cobra.Command{
		Use:   "wait",
		Short: "Wait for a turn from the attached reader",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			descriptor, err := readBridgeDescriptor(descriptorPath)
			if err != nil {
				return err
			}
			values := url.Values{}
			values.Set("attachment_id", descriptor.AttachmentID)
			return bridgeRequest(command.Context(), client, http.MethodGet, descriptor.Endpoint+"/task/turns/wait?"+values.Encode(), descriptor.TaskSecret, nil, stdout)
		},
	}
	command.Flags().StringVar(&descriptorPath, "descriptor", "", "owner-only reader bridge descriptor")
	_ = command.MarkFlagRequired("descriptor")
	return command
}

func newBridgePublishCommand(stdout io.Writer, client *http.Client) *cobra.Command {
	var descriptorPath string
	command := &cobra.Command{
		Use:   "publish",
		Short: "Publish one normalized event to the attached reader",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			descriptor, err := readBridgeDescriptor(descriptorPath)
			if err != nil {
				return err
			}
			body, err := io.ReadAll(io.LimitReader(command.InOrStdin(), (64<<10)+1))
			if err != nil {
				return err
			}
			if len(body) > 64<<10 {
				return errors.New("event exceeds 64 KiB")
			}
			return bridgeRequest(command.Context(), client, http.MethodPost, descriptor.Endpoint+"/task/events", descriptor.TaskSecret, body, stdout)
		},
	}
	command.Flags().StringVar(&descriptorPath, "descriptor", "", "owner-only reader bridge descriptor")
	_ = command.MarkFlagRequired("descriptor")
	return command
}

func readBridgeDescriptor(path string) (agentbridge.Descriptor, error) {
	var descriptor agentbridge.Descriptor
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if err != nil {
		return descriptor, fmt.Errorf("reading bridge descriptor: %w", err)
	}
	if !info.Mode().IsRegular() {
		return descriptor, errors.New("bridge descriptor must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return descriptor, errors.New("bridge descriptor must be owner-only")
	}
	file, err := os.Open(path)
	if err != nil {
		return descriptor, fmt.Errorf("opening bridge descriptor: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&descriptor); err != nil {
		return descriptor, fmt.Errorf("decoding bridge descriptor: %w", err)
	}
	if descriptor.Version != agentbridge.ProtocolVersion {
		return descriptor, fmt.Errorf("unsupported bridge protocol %q", descriptor.Version)
	}
	if descriptor.AttachmentID == "" || descriptor.Endpoint == "" || descriptor.TaskSecret == "" {
		return descriptor, errors.New("bridge descriptor is incomplete")
	}
	endpoint, err := url.Parse(descriptor.Endpoint)
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" {
		return descriptor, errors.New("bridge descriptor endpoint must use loopback HTTP")
	}
	return descriptor, nil
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
