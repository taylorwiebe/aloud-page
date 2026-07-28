package skills

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/taylorwiebe/planreader/internal/install"
)

func TestEmbeddedSkillIsRepositoryIndependent(t *testing.T) {
	for _, name := range []string{"read-with-planreader/SKILL.md", "read-with-planreader/agents/openai.yaml"} {
		if _, err := fs.ReadFile(Files, name); err != nil {
			t.Fatalf("embedded %s: %v", name, err)
		}
	}
	data, err := fs.ReadFile(Files, "read-with-planreader/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "planreader ") || !strings.Contains(text, "--provider PROVIDER") || strings.Contains(text, "go run") || strings.Contains(text, "git rev-parse") {
		t.Fatalf("skill is not installed-command based:\n%s", text)
	}
}

func TestInstallAndUpdateDistributeCurrentEmbeddedConversationLoop(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "planreader")
	if err := os.WriteFile(executable, []byte("test executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	service := install.Service{
		Home: home, GOOS: "darwin", GOARCH: "arm64", Executable: executable,
		Version: "1.0.0", Origin: "release", Skill: Files,
	}
	if _, err := service.Install(); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(home, ".codex", "skills", "read-with-planreader", "SKILL.md")
	if err := os.WriteFile(installed, []byte("older managed skill"), 0o600); err != nil {
		t.Fatal(err)
	}
	service.Version = "1.0.1"
	if _, err := service.Install(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	want, err := fs.ReadFile(Files, "read-with-planreader/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) || !strings.Contains(string(got), "capability handshake") {
		t.Fatal("install/update left an older embedded skill loop")
	}
}

func TestEmbeddedSkillDefinesFailClosedCurrentTaskBridgeLoop(t *testing.T) {
	data, err := fs.ReadFile(Files, "read-with-planreader/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{
		"capability handshake",
		"Claude is currently unsupported",
		"--descriptor PATH",
		"never print or read the descriptor contents",
		"conversation, working directory, project instructions, tools, and native",
		"`progress`, `text`, `proposal`",
		"`completed`, `failed`, `cancelled`, or",
		"`authorization_denied`",
		"unknown provider-native events",
		"strictly increasing sequence numbers",
		"same descriptor, attachment identity, task secret",
		"confirm that its private descriptor was removed",
		"Prepared narration has no verified authoritative source identity",
		"require an attached, supported current task",
		"source or repository changes are unavailable",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("embedded conversation loop missing %q", required)
		}
	}
	for _, forbidden := range []string{"--secret", "--endpoint", "session resume", "start a replacement"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("embedded conversation loop contains forbidden fallback or credential flag %q", forbidden)
		}
	}
}
