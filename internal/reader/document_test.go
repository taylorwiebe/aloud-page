package reader

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/taylorwiebe/planreader/internal/narration"
)

func testDocumentState(t *testing.T, source string, friendly narration.Narration) *DocumentState {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := NewDocumentState(path, []byte(source), narration.SplitMarkdownSections(source), friendly)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestResolveOriginalSelectionExactRanges(t *testing.T) {
	source := "# First\n\nRepeat café.\nRepeat café.\n\n## Code\n\n```go\nfmt.Println(\"café\")\n```\n"
	friendly := narration.Narration{Title: "Friendly", Sections: []narration.NarrationSection{{
		ID: "first", Heading: "First", SourceSectionIDs: []string{"source-0"}, Sentences: []string{"Friendly."},
	}}}
	state := testDocumentState(t, source, friendly)
	sections := narration.SplitMarkdownSections(source)

	tests := []struct {
		name, sectionID, quote string
		occurrence             int
	}{
		{name: "ASCII", sectionID: "source-0", quote: "Repeat", occurrence: 0},
		{name: "Unicode", sectionID: "source-0", quote: "café", occurrence: 1},
		{name: "Repeated", sectionID: "source-0", quote: "Repeat café.", occurrence: 1},
		{name: "Fenced", sectionID: "source-1", quote: `fmt.Println("café")`, occurrence: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			section := sections[0]
			if tc.sectionID == "source-1" {
				section = sections[1]
			}
			start := nthByteIndex(section.Markdown, tc.quote, tc.occurrence)
			got, err := state.ResolveOriginalSelection(OriginalSelectionRequest{
				Revision: state.Revision(), SectionID: tc.sectionID, Quote: tc.quote,
				StartOffset: start, EndOffset: start + len(tc.quote),
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.StartByte != section.StartByte+start || got.EndByte != section.StartByte+start+len(tc.quote) {
				t.Fatalf("range = %d-%d, want %d-%d", got.StartByte, got.EndByte, section.StartByte+start, section.StartByte+start+len(tc.quote))
			}
			if got.Quote != tc.quote || got.StartLine < section.StartLine || got.EndLine < got.StartLine {
				t.Fatalf("resolved selection = %#v", got)
			}
		})
	}
}

func TestResolveOriginalSelectionRejectsInvalidAndStaleAnchors(t *testing.T) {
	source := "# One\n\nAlpha.\n\n# Two\n\nBeta.\n"
	state := testDocumentState(t, source, narration.Narration{Title: "Friendly", Sections: []narration.NarrationSection{{
		ID: "one", Heading: "One", SourceSectionIDs: []string{"source-0"}, Sentences: []string{"Alpha."},
	}}})
	base := OriginalSelectionRequest{Revision: state.Revision(), SectionID: "source-0", Quote: "Alpha.", StartOffset: 7, EndOffset: 13}
	tests := []struct {
		name string
		edit func(*OriginalSelectionRequest)
		want error
	}{
		{"empty", func(r *OriginalSelectionRequest) { r.Quote = ""; r.EndOffset = r.StartOffset }, ErrInvalidSelection},
		{"whitespace", func(r *OriginalSelectionRequest) { r.Quote = "\n"; r.StartOffset = 5; r.EndOffset = 6 }, ErrInvalidSelection},
		{"wrong quote", func(r *OriginalSelectionRequest) { r.Quote = "Beta."; r.EndOffset = r.StartOffset + len(r.Quote) }, ErrAmbiguousSelection},
		{"cross section", func(r *OriginalSelectionRequest) { r.EndOffset = len(source) }, ErrInvalidSelection},
		{"stale", func(r *OriginalSelectionRequest) { r.Revision = "old" }, ErrStaleRevision},
		{"oversized", func(r *OriginalSelectionRequest) {
			r.Quote = strings.Repeat("x", maxSelectionBytes+1)
			r.EndOffset = r.StartOffset + len(r.Quote)
		}, ErrInvalidSelection},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := base
			tc.edit(&request)
			if _, err := state.ResolveOriginalSelection(request); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestResolveFriendlySelectionReturnsOnlyCandidateSections(t *testing.T) {
	source := "# One\n\nAlpha.\n\n# Two\n\nBeta.\n"
	friendly := narration.Narration{Title: "Friendly", Sections: []narration.NarrationSection{{
		ID: "summary", Heading: "Summary", SourceSectionIDs: []string{"source-0", "source-1"},
		Sentences: []string{"Alpha and beta are related.", "Keep both."},
	}}}
	state := testDocumentState(t, source, friendly)
	got, err := state.ResolveFriendlySelection(FriendlySelectionRequest{
		Revision: state.FriendlyRevision(), SectionID: "summary", BlockIndex: 0,
		Quote: "beta", StartOffset: 10, EndOffset: 14,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Quote != "beta" || !reflect.DeepEqual(got.CandidateSourceSectionIDs, []string{"source-0", "source-1"}) {
		t.Fatalf("resolved friendly selection = %#v", got)
	}
}

func TestDocumentStateDetectsExternalEdit(t *testing.T) {
	state := testDocumentState(t, "# One\n\nAlpha.\n", narration.Narration{Title: "Friendly", Sections: []narration.NarrationSection{{
		ID: "one", Heading: "One", SourceSectionIDs: []string{"source-0"}, Sentences: []string{"Alpha."},
	}}})
	if err := os.WriteFile(state.canonicalPath, []byte("# One\n\nChanged.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := state.VerifyCurrentSource(); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("VerifyCurrentSource() error = %v, want stale revision", err)
	}
	if _, err := state.ResolveOriginalSelection(OriginalSelectionRequest{
		Revision: state.Revision(), SectionID: "source-0", Quote: "Alpha.", StartOffset: 7, EndOffset: 13,
	}); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("ResolveOriginalSelection() error = %v, want stale revision", err)
	}
}

func nthByteIndex(value, quote string, occurrence int) int {
	offset := 0
	for i := 0; i <= occurrence; i++ {
		found := strings.Index(value[offset:], quote)
		if found < 0 {
			return -1
		}
		offset += found
		if i < occurrence {
			offset += len(quote)
		}
	}
	return offset
}
