package reader

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/taylorwiebe/planreader/internal/narration"
)

func TestApplyApprovedSourceChangeRejectsSourceConflictBeforeExecution(t *testing.T) {
	state := testDocumentState(t, "# One\n\nAlpha.\n", friendlyForSource("Alpha."))
	baseDigest := state.SourceDigest()
	if err := os.WriteFile(state.canonicalPath, []byte("# One\n\nExternal.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	applied := false
	_, err := state.ApplyApprovedSourceChange(context.Background(), ApprovedSourceChange{
		ProposalID: "proposal-1", ProposalDigest: "proposal-digest", BaseSourceDigest: baseDigest, ChangesPlan: true,
	}, func(context.Context) error {
		applied = true
		return nil
	}, func(context.Context, string, []narration.SourceSection) (narration.Narration, error) {
		t.Fatal("regeneration ran for a conflicted source")
		return narration.Narration{}, nil
	})
	if !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("ApplyApprovedSourceChange() error = %v, want stale revision", err)
	}
	if applied {
		t.Fatal("source change executed after its base source conflicted")
	}
}

func TestApplyApprovedSourceChangeKeepsNewSourceAndMarksFriendlyStaleWhenRegenerationFails(t *testing.T) {
	state := testDocumentState(t, "# One\n\nAlpha.\n", friendlyForSource("Alpha."))
	oldFriendlyRevision := state.FriendlyRevision()
	result, err := state.ApplyApprovedSourceChange(context.Background(), ApprovedSourceChange{
		ProposalID: "proposal-1", ProposalDigest: "proposal-digest", BaseSourceDigest: state.SourceDigest(), ChangesPlan: true,
	}, func(context.Context) error {
		return os.WriteFile(state.canonicalPath, []byte("# One\n\nChanged.\n"), 0o600)
	}, func(context.Context, string, []narration.SourceSection) (narration.Narration, error) {
		return narration.Narration{}, errors.New("provider failed")
	})
	if err == nil {
		t.Fatal("ApplyApprovedSourceChange() error = nil, want regeneration failure")
	}
	if result.Source != "# One\n\nChanged.\n" || !result.FriendlyStale {
		t.Fatalf("result = %#v, want changed source with stale friendly content", result)
	}
	if result.FriendlyRevision != oldFriendlyRevision {
		t.Fatalf("friendly revision = %q, want preserved %q", result.FriendlyRevision, oldFriendlyRevision)
	}
	retried, retryErr := state.RetryFriendlyNarration(context.Background(), result.SourceDigest,
		func(_ context.Context, markdown string, sources []narration.SourceSection) (narration.Narration, error) {
			if markdown != result.Source {
				t.Fatalf("retry markdown = %q, want %q", markdown, result.Source)
			}
			return friendlyForSource("Changed."), nil
		})
	if retryErr != nil {
		t.Fatal(retryErr)
	}
	if retried.FriendlyStale || retried.FriendlyRevision == oldFriendlyRevision {
		t.Fatalf("retried snapshot = %#v, want a fresh friendly revision", retried)
	}
}

func TestApplyApprovedSourceChangePublishesBothRepresentationsOnce(t *testing.T) {
	state := testDocumentState(t, "# One\n\nAlpha.\n", friendlyForSource("Alpha."))
	oldRevision, oldFriendlyRevision := state.Revision(), state.FriendlyRevision()
	applies, generations := 0, 0
	change := ApprovedSourceChange{
		ProposalID: "proposal-1", ProposalDigest: "proposal-digest", BaseSourceDigest: state.SourceDigest(), ChangesPlan: true,
	}
	result, err := state.ApplyApprovedSourceChange(context.Background(), change, func(context.Context) error {
		applies++
		return os.WriteFile(state.canonicalPath, []byte("# One\n\nChanged.\n"), 0o600)
	}, func(_ context.Context, _ string, _ []narration.SourceSection) (narration.Narration, error) {
		generations++
		return friendlyForSource("Changed."), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision == oldRevision || result.FriendlyRevision == oldFriendlyRevision || result.FriendlyStale {
		t.Fatalf("published snapshot = %#v", result)
	}
	if result.Narration.Sections[0].Sentences[0] != "Changed." || applies != 1 || generations != 1 {
		t.Fatalf("applies = %d, generations = %d, narration = %#v", applies, generations, result.Narration)
	}
	_, err = state.ApplyApprovedSourceChange(context.Background(), change, func(context.Context) error {
		applies++
		return nil
	}, nil)
	if !errors.Is(err, ErrAlreadyApplied) || applies != 1 {
		t.Fatalf("duplicate error = %v, applies = %d", err, applies)
	}
}

func TestApplyApprovedSourceChangeRejectsChangedProposalDigest(t *testing.T) {
	state := testDocumentState(t, "# One\n\nAlpha.\n", friendlyForSource("Alpha."))
	change := ApprovedSourceChange{
		ProposalID: "proposal-1", ProposalDigest: "digest-1", BaseSourceDigest: state.SourceDigest(), ChangesPlan: false,
	}
	if _, err := state.ApplyApprovedSourceChange(context.Background(), change, func(context.Context) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	change.ProposalDigest = "digest-2"
	if _, err := state.ApplyApprovedSourceChange(context.Background(), change, func(context.Context) error {
		t.Fatal("changed proposal digest replayed the action")
		return nil
	}, nil); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("changed proposal digest error = %v, want stale revision", err)
	}
}

func TestApplyApprovedSourceChangeRejectsInvalidRegeneratedMappings(t *testing.T) {
	state := testDocumentState(t, "# One\n\nAlpha.\n", friendlyForSource("Alpha."))
	oldFriendlyRevision := state.FriendlyRevision()
	result, err := state.ApplyApprovedSourceChange(context.Background(), ApprovedSourceChange{
		ProposalID: "proposal-1", ProposalDigest: "digest-1", BaseSourceDigest: state.SourceDigest(), ChangesPlan: true,
	}, func(context.Context) error {
		return os.WriteFile(state.canonicalPath, []byte("# One\n\nChanged.\n"), 0o600)
	}, func(context.Context, string, []narration.SourceSection) (narration.Narration, error) {
		invalid := friendlyForSource("Changed.")
		invalid.Sections[0].SourceSectionIDs = []string{"missing"}
		return invalid, nil
	})
	if !errors.Is(err, ErrFriendlyStale) || !result.FriendlyStale || result.FriendlyRevision != oldFriendlyRevision {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestApplyApprovedRepositoryChangeSkipsNarrationWhenPlanIsUnchanged(t *testing.T) {
	state := testDocumentState(t, "# One\n\nAlpha.\n", friendlyForSource("Alpha."))
	before := state.Snapshot()
	applied := false
	result, err := state.ApplyApprovedSourceChange(context.Background(), ApprovedSourceChange{
		ProposalID: "proposal-1", ProposalDigest: "digest-1", BaseSourceDigest: state.SourceDigest(), ChangesPlan: false,
	}, func(context.Context) error {
		applied = true
		return nil
	}, func(context.Context, string, []narration.SourceSection) (narration.Narration, error) {
		t.Fatal("narration ran for a non-plan repository change")
		return narration.Narration{}, nil
	})
	if err != nil || !applied || result.Revision != before.Revision || result.FriendlyRevision != before.FriendlyRevision {
		t.Fatalf("result = %#v, applied = %v, error = %v", result, applied, err)
	}
}

func TestApplyApprovedSourceChangeRejectsUnsafeAuthoritativeSourceBeforeExecution(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{"deleted", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		{"non-regular", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"oversized", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(strings.Repeat("x", MaxDocumentBytes+1)), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := testDocumentState(t, "# One\n\nAlpha.\n", friendlyForSource("Alpha."))
			tc.mutate(t, state.canonicalPath)
			applied := false
			_, err := state.ApplyApprovedSourceChange(context.Background(), ApprovedSourceChange{
				ProposalID: "proposal-1", ProposalDigest: "digest-1", BaseSourceDigest: state.SourceDigest(), ChangesPlan: true,
			}, func(context.Context) error { applied = true; return nil }, nil)
			if err == nil || applied {
				t.Fatalf("error = %v, applied = %v", err, applied)
			}
		})
	}
}

func friendlyForSource(sentence string) narration.Narration {
	return narration.Narration{Title: "Friendly", Sections: []narration.NarrationSection{{
		ID: "one", Heading: "One", SourceSectionIDs: []string{"source-0"}, Sentences: []string{sentence},
	}}}
}

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
