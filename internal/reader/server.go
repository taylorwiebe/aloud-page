package reader

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"github.com/taylorwiebe/planreader/internal/agentbridge"
	"github.com/taylorwiebe/planreader/internal/narration"
	"github.com/taylorwiebe/planreader/internal/speech"
)

//go:embed web/*
var webFiles embed.FS

type ReaderDocument struct {
	FileName         string                  `json:"file_name"`
	Narration        narration.Narration     `json:"narration"`
	Sources          []RenderedSourceSection `json:"sources"`
	DocumentRevision string                  `json:"document_revision,omitempty"`
	FriendlyRevision string                  `json:"friendly_revision,omitempty"`
	CanEdit          bool                    `json:"can_edit"`
	AgentManaged     bool                    `json:"agent_managed,omitempty"`
	State            *DocumentState          `json:"-"`
}

type RenderedSourceSection struct {
	ID      string `json:"id"`
	Heading string `json:"heading"`
	Level   int    `json:"level"`
	HTML    string `json:"html"`
}

func RenderSourceSections(sections []narration.SourceSection) ([]RenderedSourceSection, error) {
	markdown := goldmark.New(goldmark.WithExtensions(extension.GFM))
	rendered := make([]RenderedSourceSection, 0, len(sections))
	for _, section := range sections {
		var output bytes.Buffer
		if err := markdown.Convert([]byte(section.Markdown), &output); err != nil {
			return nil, fmt.Errorf("rendering source section %q: %w", section.Heading, err)
		}
		rendered = append(rendered, RenderedSourceSection{
			ID:      section.ID,
			Heading: section.Heading,
			Level:   section.Level,
			HTML:    fmt.Sprintf(`<div data-source-anchor="%s">%s</div>`, section.ID, output.String()),
		})
	}
	return rendered, nil
}

func newReaderHandler(document ReaderDocument, token string) http.Handler {
	return newReaderHandlerWithSpeechAndLifecycle(document, token, nil, nil)
}

func newReaderHandlerWithSpeech(document ReaderDocument, token string, speechService *speech.Service) http.Handler {
	return newReaderHandlerWithSpeechAndLifecycle(document, token, speechService, nil)
}

func newReaderHandlerWithSpeechAndLifecycle(document ReaderDocument, token string, speechService *speech.Service, lifecycle *agentLifecycle) http.Handler {
	return newReaderHandlerWithBridge(document, token, speechService, lifecycle, nil)
}

func newReaderHandlerWithBridge(document ReaderDocument, token string, speechService *speech.Service, lifecycle *agentLifecycle, bridge http.Handler) http.Handler {
	prefix := "/reader/" + token + "/"
	document.AgentManaged = lifecycle != nil
	data, err := json.Marshal(document)
	if err != nil {
		panic(fmt.Sprintf("encoding reader document: %v", err))
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asset, ok := strings.CutPrefix(r.URL.Path, prefix)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if asset == "" {
			asset = "index.html"
		}

		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; media-src 'self' blob:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")

		if lifecycle != nil && strings.HasPrefix(asset, "api/agent/") {
			lifecycle.ServeHTTP(w, r, strings.TrimPrefix(asset, "api/"))
			return
		}
		if bridge != nil && strings.HasPrefix(asset, "api/conversation/") {
			r.URL.Path = "/" + strings.TrimPrefix(asset, "api/conversation/")
			bridge.ServeHTTP(w, r)
			return
		}
		if speechService != nil && strings.HasPrefix(asset, "api/") {
			speechService.HandleAPI(w, r, strings.TrimPrefix(asset, "api/"), prefix)
			return
		}
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}

		if asset == "data.json" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write(data)
			return
		}

		content, err := fs.ReadFile(webFiles, "web/"+asset)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch {
		case strings.HasSuffix(asset, ".html"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case strings.HasSuffix(asset, ".css"):
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		case strings.HasSuffix(asset, ".js"):
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		}
		_, _ = w.Write(content)
	})
}

func StartServer(document ReaderDocument, shutdownRequested func()) (string, *http.Server, error) {
	url, _, server, err := startServer(document, shutdownRequested)
	return url, server, err
}

func StartAttachedServer(document ReaderDocument, shutdownRequested func()) (string, agentbridge.Descriptor, *http.Server, error) {
	return startServer(document, shutdownRequested)
}

func startServer(document ReaderDocument, shutdownRequested func()) (string, agentbridge.Descriptor, *http.Server, error) {
	var descriptor agentbridge.Descriptor
	speechService, err := speech.NewService()
	if err != nil {
		return "", descriptor, nil, err
	}
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		speechService.Close()
		return "", descriptor, nil, fmt.Errorf("creating local access token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		speechService.Close()
		return "", descriptor, nil, fmt.Errorf("starting local reader: %w", err)
	}
	var lifecycle *agentLifecycle
	var bridge http.Handler
	var broker *agentbridge.Broker
	if shutdownRequested != nil {
		lifecycle = newAgentLifecycle(shutdownRequested, 2*time.Minute, 12*time.Hour)
		attachmentBytes := make([]byte, 24)
		taskSecretBytes := make([]byte, 32)
		if _, err := rand.Read(attachmentBytes); err != nil {
			listener.Close()
			speechService.Close()
			return "", descriptor, nil, fmt.Errorf("creating attachment identity: %w", err)
		}
		if _, err := rand.Read(taskSecretBytes); err != nil {
			listener.Close()
			speechService.Close()
			return "", descriptor, nil, fmt.Errorf("creating task secret: %w", err)
		}
		descriptor = agentbridge.Descriptor{
			Version:      agentbridge.ProtocolVersion,
			AttachmentID: hex.EncodeToString(attachmentBytes),
			TaskSecret:   hex.EncodeToString(taskSecretBytes),
		}
		broker = agentbridge.NewBrokerWithLeases(descriptor.AttachmentID, descriptor.TaskSecret, 5*time.Second, 2*time.Minute)
		lifecycle.browserLease = broker
		bridge = agentbridge.NewHTTPHandler(broker, "")
	}
	server := &http.Server{
		Handler:           newReaderHandlerWithBridge(document, token, speechService, lifecycle, bridge),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		_ = server.Serve(listener)
		if lifecycle != nil {
			lifecycle.Close()
		}
		if broker != nil {
			broker.Close()
		}
		speechService.Close()
	}()

	url := fmt.Sprintf("http://%s/reader/%s/", listener.Addr().String(), token)
	descriptor.Endpoint = strings.TrimRight(url, "/") + "/api/conversation"
	return url, descriptor, server, nil
}

func OpenBrowser(url string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{url}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		command, args = "xdg-open", []string{url}
	}
	cmd := exec.Command(command, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("opening browser: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
