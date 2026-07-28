package narration

import (
	"fmt"
	"regexp"
	"strings"
)

var markdownHeading = regexp.MustCompile(`^(#{1,6})[\t ]+(.+?)[\t ]*#*[\t ]*$`)

type SourceSection struct {
	ID        string `json:"id"`
	Heading   string `json:"heading"`
	Level     int    `json:"level"`
	Markdown  string `json:"markdown"`
	StartByte int    `json:"-"`
	EndByte   int    `json:"-"`
	StartLine int    `json:"-"`
	EndLine   int    `json:"-"`
}

func splitMarkdownSections(markdown string) []SourceSection {
	contentStart := yamlFrontMatterEnd(markdown)
	sections := make([]SourceSection, 0)
	var current *SourceSection
	inFence := false

	flush := func(endByte int) {
		if current == nil || endByte <= current.StartByte || strings.TrimSpace(markdown[current.StartByte:endByte]) == "" {
			return
		}
		current.EndByte = endByte
		current.EndLine = lineNumberAt(markdown, lastContentByte(markdown, current.StartByte, endByte))
		current.Markdown = markdown[current.StartByte:current.EndByte]
		current.ID = fmt.Sprintf("source-%d", len(sections))
		sections = append(sections, *current)
	}

	for offset := contentStart; offset < len(markdown); {
		lineEnd := strings.IndexByte(markdown[offset:], '\n')
		next := len(markdown)
		if lineEnd >= 0 {
			next = offset + lineEnd + 1
		}
		line := strings.TrimSuffix(markdown[offset:next], "\n")
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		}

		match := markdownHeading.FindStringSubmatch(line)
		if !inFence && len(match) == 3 {
			flush(offset)
			current = &SourceSection{
				Heading:   strings.TrimSpace(match[2]),
				Level:     len(match[1]),
				StartByte: offset,
				StartLine: lineNumberAt(markdown, offset),
			}
		}
		if current == nil {
			if strings.TrimSpace(line) == "" {
				offset = next
				continue
			}
			current = &SourceSection{
				Heading:   "Introduction",
				Level:     1,
				StartByte: offset,
				StartLine: lineNumberAt(markdown, offset),
			}
		}
		offset = next
	}
	flush(len(markdown))

	if len(sections) == 0 && strings.TrimSpace(markdown[contentStart:]) != "" {
		sections = append(sections, SourceSection{
			ID:        "source-0",
			Heading:   "Introduction",
			Level:     1,
			Markdown:  markdown[contentStart:],
			StartByte: contentStart,
			EndByte:   len(markdown),
			StartLine: lineNumberAt(markdown, contentStart),
			EndLine:   lineNumberAt(markdown, lastContentByte(markdown, contentStart, len(markdown))),
		})
	}
	return sections
}

func SplitMarkdownSections(markdown string) []SourceSection {
	return splitMarkdownSections(markdown)
}

func stripYAMLFrontMatter(markdown string) string {
	start := yamlFrontMatterEnd(markdown)
	return markdown[start:]
}

func yamlFrontMatterEnd(markdown string) int {
	offset := 0
	firstEnd := strings.IndexByte(markdown, '\n')
	if firstEnd < 0 || strings.TrimSpace(strings.TrimSuffix(markdown[:firstEnd], "\r")) != "---" {
		return 0
	}
	offset = firstEnd + 1
	for offset < len(markdown) {
		lineEnd := strings.IndexByte(markdown[offset:], '\n')
		if lineEnd < 0 {
			return 0
		}
		next := offset + lineEnd + 1
		if strings.TrimSpace(strings.TrimSuffix(markdown[offset:offset+lineEnd], "\r")) == "---" {
			return next
		}
		offset = next
	}
	return 0
}

func lineNumberAt(markdown string, offset int) int {
	if offset < 0 {
		offset = 0
	}
	if offset > len(markdown) {
		offset = len(markdown)
	}
	return 1 + strings.Count(markdown[:offset], "\n")
}

func lastContentByte(markdown string, start, end int) int {
	for end > start && (markdown[end-1] == '\n' || markdown[end-1] == '\r') {
		end--
	}
	if end == start {
		return start
	}
	return end - 1
}
