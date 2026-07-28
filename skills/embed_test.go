package skills

import (
	"io/fs"
	"strings"
	"testing"
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
