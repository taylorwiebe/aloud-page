package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/taylorwiebe/planreader/internal/agentbridge"
	"github.com/taylorwiebe/planreader/internal/narration"
	"github.com/taylorwiebe/planreader/internal/reader"
)

const maxDocumentBytes = reader.MaxDocumentBytes
const maxPreparedDocumentBytes = 10 << 20

type options struct {
	depth         string
	audience      string
	provider      string
	claudeCommand string
	codexCommand  string
	timeout       time.Duration
	noOpen        bool
	prepared      string
	agentManaged  bool
}

func Execute() {
	if err := newRootCommand(os.Stdout, os.Stderr).Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "planreader: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	command := newRootCommand(stdout, stderr)
	command.SetArgs(args)
	return command.Execute()
}

func newRootCommand(stdout, stderr io.Writer) *cobra.Command {
	config := options{
		depth:         "working",
		audience:      "software developer who may not know internal systems, identity terminology, or Compound Engineering conventions",
		provider:      "claude",
		claudeCommand: "claude",
		codexCommand:  "codex",
		timeout:       3 * time.Minute,
	}
	command := &cobra.Command{
		Use:           "planreader [flags] DOCUMENT.md",
		Short:         "Turn Markdown into a clear, private spoken companion",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args: func(_ *cobra.Command, args []string) error {
			if config.prepared != "" && len(args) != 0 {
				return errors.New("do not provide a Markdown document with --prepared")
			}
			if config.prepared == "" && len(args) != 1 {
				return errors.New("provide exactly one Markdown document")
			}
			return nil
		},
		RunE: func(_ *cobra.Command, args []string) error {
			return runReader(config, args, stdout, stderr)
		},
	}
	command.SetOut(stdout)
	command.SetErr(stderr)
	flags := command.Flags()
	flags.StringVar(&config.depth, "depth", config.depth, "narration depth: briefing, working, or full")
	flags.StringVar(&config.audience, "audience", config.audience, "what the narration may assume about the listener")
	flags.StringVar(&config.provider, "provider", config.provider, "approved AI provider: claude or codex")
	flags.StringVar(&config.claudeCommand, "claude", config.claudeCommand, "path to the company-authenticated Claude Code executable")
	flags.StringVar(&config.codexCommand, "codex", config.codexCommand, "path to the company-authenticated Codex executable")
	flags.DurationVar(&config.timeout, "timeout", config.timeout, "maximum time to wait for the AI provider")
	flags.BoolVar(&config.noOpen, "no-open", false, "print the reader URL without opening a browser")
	flags.StringVar(&config.prepared, "prepared", "", "reuse a previously prepared Planreader data.json without calling an AI provider")
	flags.BoolVar(&config.agentManaged, "agent-managed", false, "replace older agent readers and stop when the browser is gone")
	command.AddCommand(newVersionCommand(stdout), newInstallCommand(stdout), newUpdateCommand(stdout), newBridgeCommand(stdout))
	return command
}

func runReader(config options, args []string, stdout, stderr io.Writer) error {
	if config.prepared != "" {
		document, err := readPreparedDocument(config.prepared)
		if err != nil {
			return err
		}
		return serveReader(document, config.noOpen, config.agentManaged, stdout, stderr)
	}

	depthPrompt, ok := map[string]string{
		"briefing": "a quick briefing of about 3 to 5 minutes",
		"working":  "a working understanding of about 10 to 15 minutes",
		"full":     "a complete spoken companion that keeps all consequential detail",
	}[config.depth]
	if !ok {
		return fmt.Errorf("unknown depth %q; use briefing, working, or full", config.depth)
	}
	if strings.TrimSpace(config.audience) == "" {
		return errors.New("audience cannot be empty")
	}

	filePath, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("resolving document path: %w", err)
	}
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("opening document: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspecting document: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("document must be a regular file")
	}
	if info.Size() > maxDocumentBytes {
		return fmt.Errorf("document is %.1f MB; the current limit is 2 MB", float64(info.Size())/(1<<20))
	}
	markdown, err := io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	if err != nil {
		return fmt.Errorf("reading document: %w", err)
	}
	if len(markdown) > maxDocumentBytes {
		return errors.New("document exceeds the current 2 MB limit")
	}
	if strings.TrimSpace(string(markdown)) == "" {
		return errors.New("document is empty")
	}
	canonicalPath, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		return fmt.Errorf("resolving canonical document path: %w", err)
	}

	providerCommand := config.claudeCommand
	if config.provider == "codex" {
		providerCommand = config.codexCommand
	} else if config.provider != "claude" {
		return fmt.Errorf("unknown provider %q; use claude or codex", config.provider)
	}
	resolvedCommand, err := exec.LookPath(providerCommand)
	if err != nil {
		return fmt.Errorf("finding %s: %w; install it and sign in with the company-approved account", config.provider, err)
	}

	sources := narration.SplitMarkdownSections(string(markdown))
	renderedSources, err := reader.RenderSourceSections(sources)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(stdout, "Creating a %s narration with %s…\n", config.depth, config.provider)
	var runner interface {
		Generate(context.Context, string, narration.ReaderOptions) (narration.Narration, error)
	}
	if config.provider == "codex" {
		runner = narration.CodexRunner{Command: resolvedCommand, Timeout: config.timeout}
	} else {
		runner = narration.ClaudeRunner{Command: resolvedCommand, Timeout: config.timeout}
	}
	generated, err := runner.Generate(ctx, string(markdown), narration.ReaderOptions{
		Depth:    depthPrompt,
		Audience: config.audience,
		Sections: sources,
	})
	if err != nil {
		return err
	}
	if err := narration.ValidateSourceMappings(generated, sources); err != nil {
		return fmt.Errorf("Claude returned an invalid source map: %w", err)
	}

	document := reader.ReaderDocument{
		FileName:  filepath.Base(filePath),
		Narration: generated,
		Sources:   renderedSources,
	}
	document.State, err = reader.NewDocumentState(canonicalPath, markdown, sources, generated)
	if err != nil {
		return fmt.Errorf("preparing authoritative document state: %w", err)
	}
	if err := document.State.VerifyCurrentSource(); err != nil {
		return fmt.Errorf("verifying authoritative document state: %w", err)
	}
	document.DocumentRevision = document.State.Revision()
	document.FriendlyRevision = document.State.FriendlyRevision()
	document.CanEdit = config.agentManaged
	document.Regenerate = func(regenerationContext context.Context, markdown string, sections []narration.SourceSection) (narration.Narration, error) {
		return runner.Generate(regenerationContext, markdown, narration.ReaderOptions{
			Depth: depthPrompt, Audience: config.audience, Sections: sections,
		})
	}
	return serveReader(document, config.noOpen, config.agentManaged, stdout, stderr)
}

func readPreparedDocument(path string) (reader.ReaderDocument, error) {
	var document reader.ReaderDocument
	file, err := os.Open(path)
	if err != nil {
		return document, fmt.Errorf("opening prepared reader data: %w", err)
	}
	defer file.Close()
	if err := json.NewDecoder(io.LimitReader(file, maxPreparedDocumentBytes+1)).Decode(&document); err != nil {
		return document, fmt.Errorf("decoding prepared reader data: %w", err)
	}
	if strings.TrimSpace(document.FileName) == "" || strings.TrimSpace(document.Narration.Title) == "" || len(document.Narration.Sections) == 0 || len(document.Sources) == 0 {
		return document, errors.New("prepared reader data is incomplete")
	}
	// Prepared payloads contain presentation data only. They remain readable, but
	// cannot claim edit capability without a separately verified source identity.
	document.State = nil
	document.DocumentRevision = ""
	document.FriendlyRevision = ""
	document.CanEdit = false
	return document, nil
}

func serveReader(document reader.ReaderDocument, noOpen, agentManaged bool, stdout, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	agentShutdown := make(chan struct{})
	var shutdownOnce sync.Once
	var shutdownRequested func()
	if agentManaged {
		shutdownRequested = func() { shutdownOnce.Do(func() { close(agentShutdown) }) }
	}
	var url string
	var server *http.Server
	var descriptor agentbridge.Descriptor
	var err error
	if agentManaged {
		url, descriptor, server, err = reader.StartAttachedServer(document, shutdownRequested)
	} else {
		url, server, err = reader.StartServer(document, shutdownRequested)
	}
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if agentManaged {
		descriptorPath, descriptorCleanup, descriptorErr := writeBridgeDescriptor(descriptor)
		if descriptorErr != nil {
			return descriptorErr
		}
		defer descriptorCleanup()
		fmt.Fprintf(stdout, "Agent bridge descriptor: %s\n", descriptorPath)
		cleanup, err := claimAgentReader(url)
		if err != nil {
			fmt.Fprintf(stderr, "Could not register agent-managed cleanup: %v\n", err)
		} else {
			defer cleanup()
		}
	}

	fmt.Fprintf(stdout, "Reader ready: %s\n", url)
	fmt.Fprintln(stdout, "Press Control-C when you are finished.")
	startUpdateNotice(stdout)
	if !noOpen {
		if err := reader.OpenBrowser(url); err != nil {
			fmt.Fprintf(stderr, "Could not open the browser automatically: %v\n", err)
		}
	}

	select {
	case <-ctx.Done():
	case <-agentShutdown:
		return nil
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	return nil
}

func writeBridgeDescriptor(descriptor agentbridge.Descriptor) (string, func(), error) {
	return writeBridgeDescriptorAt("", descriptor)
}

func writeBridgeDescriptorAt(parent string, descriptor agentbridge.Descriptor) (string, func(), error) {
	dir, err := os.MkdirTemp(parent, "planreader-bridge-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating private bridge directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := os.Chmod(dir, 0o700); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("protecting private bridge directory: %w", err)
	}
	data, err := json.Marshal(descriptor)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("encoding bridge descriptor: %w", err)
	}
	path := filepath.Join(dir, "descriptor.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("writing private bridge descriptor: %w", err)
	}
	return path, cleanup, nil
}
