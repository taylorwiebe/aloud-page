package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/taylorwiebe/planreader/internal/agentbridge"
	"github.com/taylorwiebe/planreader/internal/narration"
	"github.com/taylorwiebe/planreader/internal/reader"
)

func TestClaimAgentReaderStopsPreviouslyRegisteredReader(t *testing.T) {
	registry := filepath.Join(t.TempDir(), agentReaderFile)
	previousURL := "http://127.0.0.1:4321/reader/old/"
	if err := os.WriteFile(registry, []byte(previousURL), 0o600); err != nil {
		t.Fatal(err)
	}
	var stopped string
	cleanup, err := claimAgentReaderAt(registry, "http://127.0.0.1:1234/reader/new/", func(readerURL string) {
		stopped = readerURL
	})
	if err != nil {
		t.Fatal(err)
	}
	if stopped != previousURL {
		t.Fatalf("stopped reader = %q, want %q", stopped, previousURL)
	}
	cleanup()
	if _, err := os.Stat(registry); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("registry still exists after cleanup: %v", err)
	}
}

func TestClaimAgentReaderRegistryNeverStoresBridgeSecret(t *testing.T) {
	registry := filepath.Join(t.TempDir(), agentReaderFile)
	readerURL := "http://127.0.0.1:1234/reader/browser-token/"
	cleanup, err := claimAgentReaderAt(registry, readerURL, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	data, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "task-secret") || string(data) != readerURL {
		t.Fatalf("registry leaked data beyond reader URL: %q", data)
	}
}

func TestWriteBridgeDescriptorUsesPrivateFilesAndCleanup(t *testing.T) {
	parent := t.TempDir()
	path, cleanup, err := writeBridgeDescriptorAt(parent, agentbridge.Descriptor{
		Version:      agentbridge.ProtocolVersion,
		AttachmentID: "attachment-1",
		Endpoint:     "http://127.0.0.1:1234/private",
		TaskSecret:   "task-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("descriptor mode = %o", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "task-secret") {
		t.Fatalf("descriptor = %s", data)
	}
	cleanup()
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descriptor directory remains after cleanup: %v", err)
	}
}

func TestRootCommandProvidesCobraHelp(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run([]string{"--help"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Turn Markdown into a clear, private spoken companion", "--provider", "--prepared", "--agent-managed"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("help output is missing %q: %s", expected, stdout.String())
		}
	}
}

func TestRootCommandRequiresDocumentOrPreparedData(t *testing.T) {
	if err := run(nil, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "provide exactly one Markdown document") {
		t.Fatalf("run() error = %v", err)
	}
}

func TestReadPreparedDocumentReusesCompleteReaderData(t *testing.T) {
	want := reader.ReaderDocument{
		FileName:  "plan.md",
		Narration: narration.Narration{Title: "Prepared plan", Sections: []narration.NarrationSection{{ID: "intro", Heading: "Intro", Sentences: []string{"Already written."}}}},
		Sources:   []reader.RenderedSourceSection{{ID: "source-intro", Heading: "Intro", HTML: "<p>Original</p>"}},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "data.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readPreparedDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.FileName != want.FileName || got.Narration.Title != want.Narration.Title || len(got.Sources) != 1 {
		t.Fatalf("readPreparedDocument() = %#v", got)
	}
	if got.CanEdit {
		t.Fatal("prepared reader claims edit capability without verified source identity")
	}
}

func TestReaderDocumentPayloadOmitsPrivateDocumentState(t *testing.T) {
	raw := []byte("# Hidden\n\nserver-only-marker\n")
	sections := narration.SplitMarkdownSections(string(raw))
	state, err := reader.NewDocumentState("/private/work/plan.md", raw, sections, narration.Narration{
		Title: "Plan",
		Sections: []narration.NarrationSection{{
			ID: "intro", Heading: "Intro", SourceSectionIDs: []string{"source-0"}, Sentences: []string{"Readable."},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	document := reader.ReaderDocument{
		FileName: "plan.md",
		Narration: narration.Narration{Title: "Plan", Sections: []narration.NarrationSection{{
			ID: "intro", Heading: "Intro", SourceSectionIDs: []string{"source-0"}, Sentences: []string{"Readable."},
		}}},
		Sources: []reader.RenderedSourceSection{{ID: "source-0", HTML: "<p>secret source text</p>"}},
		State:   state,
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"/private/work/plan.md", "server-only-marker", "task-secret", "canonical_path", "raw_source", "content_digest"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("browser payload leaked %q: %s", secret, data)
		}
	}
}

func TestReadPreparedDocumentRejectsIncompleteData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	if err := os.WriteFile(path, []byte(`{"file_name":"plan.md"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPreparedDocument(path); err == nil {
		t.Fatal("readPreparedDocument accepted incomplete data")
	}
}
