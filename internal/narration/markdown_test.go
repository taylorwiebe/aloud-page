package narration

import (
	"strings"
	"testing"
)

func TestSplitMarkdownSections(t *testing.T) {
	input := `Opening text.

# Main plan

Summary text.

## Requirements

- First requirement
- Second requirement
`

	got := splitMarkdownSections(input)
	if len(got) != 3 {
		t.Fatalf("len(splitMarkdownSections()) = %d, want 3", len(got))
	}
	if got[0].ID != "source-0" || got[0].Heading != "Introduction" {
		t.Fatalf("first section = %#v", got[0])
	}
	if got[1].ID != "source-1" || got[1].Heading != "Main plan" {
		t.Fatalf("second section = %#v", got[1])
	}
	if got[2].ID != "source-2" || got[2].Heading != "Requirements" {
		t.Fatalf("third section = %#v", got[2])
	}
}

func TestSplitMarkdownKeepsFencedCodeHeadingText(t *testing.T) {
	input := "# Outside\n\n```markdown\n# Not a heading\n```\n"
	got := splitMarkdownSections(input)
	if len(got) != 1 {
		t.Fatalf("len(splitMarkdownSections()) = %d, want 1", len(got))
	}
}

func TestSplitMarkdownOmitsYAMLFrontMatter(t *testing.T) {
	input := "---\ntitle: Secret plan\ntype: feat\n---\n\n# Visible title\n\nBody.\n"
	got := splitMarkdownSections(input)
	if len(got) != 1 {
		t.Fatalf("len(splitMarkdownSections()) = %d, want 1", len(got))
	}
	if strings.Contains(got[0].Markdown, "title: Secret plan") {
		t.Fatalf("section contains frontmatter: %q", got[0].Markdown)
	}
}

func TestSplitMarkdownSectionsRetainsExactSourceRanges(t *testing.T) {
	input := "---\ntitle: Plan\n---\n\n# One\n\nCafé.\n\n## Two\n\n```markdown\n# Still code\n```\n"
	got := splitMarkdownSections(input)
	if len(got) != 2 {
		t.Fatalf("len(splitMarkdownSections()) = %d, want 2", len(got))
	}
	for _, section := range got {
		if section.StartByte < 0 || section.EndByte <= section.StartByte {
			t.Fatalf("invalid range for %#v", section)
		}
		if input[section.StartByte:section.EndByte] != section.Markdown {
			t.Fatalf("section %q range = %q, markdown = %q", section.ID, input[section.StartByte:section.EndByte], section.Markdown)
		}
	}
	if got[0].StartLine != 5 || got[0].EndLine != 7 {
		t.Fatalf("first section lines = %d-%d, want 5-7", got[0].StartLine, got[0].EndLine)
	}
	if got[1].StartLine != 9 || got[1].EndLine != 13 {
		t.Fatalf("second section lines = %d-%d, want 9-13", got[1].StartLine, got[1].EndLine)
	}
}

func TestSplitMarkdownDuplicateHeadingsHaveDistinctRanges(t *testing.T) {
	input := "# Same\n\nFirst.\n\n# Same\n\nSecond.\n"
	got := splitMarkdownSections(input)
	if len(got) != 2 || got[0].ID == got[1].ID || got[0].StartByte == got[1].StartByte {
		t.Fatalf("duplicate heading sections are not distinct: %#v", got)
	}
}
