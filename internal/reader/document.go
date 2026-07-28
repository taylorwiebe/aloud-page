package reader

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/taylorwiebe/planreader/internal/narration"
)

const maxSelectionBytes = 64 << 10
const MaxDocumentBytes = 2 << 20

var (
	ErrInvalidSelection   = errors.New("invalid selection")
	ErrAmbiguousSelection = errors.New("selection does not match its anchor")
	ErrStaleRevision      = errors.New("document revision is stale")
	ErrSourceUnavailable  = errors.New("authoritative source is unavailable")
	ErrAlreadyApplied     = errors.New("source change was already applied")
	ErrFriendlyStale      = errors.New("friendly narration is stale")
)

// DocumentState contains authoritative metadata. None of its fields are serialized
// into the reader payload.
type DocumentState struct {
	mu               sync.RWMutex
	canonicalPath    string
	rawSource        []byte
	contentDigest    [sha256.Size]byte
	revision         string
	friendlyRevision string
	sections         map[string]narration.SourceSection
	narration        narration.Narration
	friendlyStale    bool
	appliedChanges   map[string]string
}

type ApprovedSourceChange struct {
	ProposalID       string
	ProposalDigest   string
	BaseSourceDigest string
	ChangesPlan      bool
}

type RegenerateNarration func(context.Context, string, []narration.SourceSection) (narration.Narration, error)

type DocumentSnapshot struct {
	Source           string
	SourceDigest     string `json:"-"`
	Revision         string
	FriendlyRevision string
	FriendlyStale    bool
	Sections         []narration.SourceSection
	Narration        narration.Narration
}

type OriginalSelectionRequest struct {
	Revision    string `json:"revision"`
	SectionID   string `json:"section_id"`
	Quote       string `json:"quote"`
	StartOffset int    `json:"start_offset"`
	EndOffset   int    `json:"end_offset"`
}

type ResolvedOriginalSelection struct {
	Representation string `json:"representation"`
	Revision       string `json:"revision"`
	SectionID      string `json:"section_id"`
	Quote          string `json:"quote"`
	StartByte      int    `json:"start_byte"`
	EndByte        int    `json:"end_byte"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
}

type FriendlySelectionRequest struct {
	Revision    string `json:"revision"`
	SectionID   string `json:"section_id"`
	BlockIndex  int    `json:"block_index"`
	Quote       string `json:"quote"`
	StartOffset int    `json:"start_offset"`
	EndOffset   int    `json:"end_offset"`
}

type ResolvedFriendlySelection struct {
	Representation            string   `json:"representation"`
	Revision                  string   `json:"revision"`
	SectionID                 string   `json:"section_id"`
	BlockIndex                int      `json:"block_index"`
	Quote                     string   `json:"quote"`
	CandidateSourceSectionIDs []string `json:"candidate_source_section_ids"`
}

func NewDocumentState(path string, raw []byte, sections []narration.SourceSection, friendly narration.Narration) (*DocumentState, error) {
	if strings.TrimSpace(path) == "" || len(raw) == 0 {
		return nil, ErrSourceUnavailable
	}
	revision, err := opaqueRevision()
	if err != nil {
		return nil, err
	}
	friendlyRevision, err := opaqueRevision()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]narration.SourceSection, len(sections))
	for _, section := range sections {
		if section.StartByte < 0 || section.EndByte > len(raw) || section.StartByte >= section.EndByte ||
			string(raw[section.StartByte:section.EndByte]) != section.Markdown {
			return nil, fmt.Errorf("source section %q has an invalid range", section.ID)
		}
		byID[section.ID] = section
	}
	return &DocumentState{
		canonicalPath:    path,
		rawSource:        append([]byte(nil), raw...),
		contentDigest:    sha256.Sum256(raw),
		revision:         revision,
		friendlyRevision: friendlyRevision,
		sections:         byID,
		narration:        friendly,
		appliedChanges:   make(map[string]string),
	}, nil
}

func (s *DocumentState) SourceDigest() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return hex.EncodeToString(s.contentDigest[:])
}

func (s *DocumentState) Snapshot() DocumentSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

func (s *DocumentState) ApplyApprovedSourceChange(ctx context.Context, change ApprovedSourceChange, apply func(context.Context) error, regenerate RegenerateNarration) (DocumentSnapshot, error) {
	if change.ProposalID == "" || change.ProposalDigest == "" || change.BaseSourceDigest == "" || apply == nil {
		return DocumentSnapshot{}, errors.New("approved source change is incomplete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prior, ok := s.appliedChanges[change.ProposalID]; ok {
		if prior == change.ProposalDigest {
			return s.snapshotLocked(), ErrAlreadyApplied
		}
		return s.snapshotLocked(), ErrStaleRevision
	}
	if hex.EncodeToString(s.contentDigest[:]) != change.BaseSourceDigest {
		return s.snapshotLocked(), ErrStaleRevision
	}
	if _, err := readAuthoritativeSource(s.canonicalPath, s.contentDigest); err != nil {
		return s.snapshotLocked(), err
	}
	if err := apply(ctx); err != nil {
		return s.snapshotLocked(), err
	}
	s.appliedChanges[change.ProposalID] = change.ProposalDigest
	if !change.ChangesPlan {
		return s.snapshotLocked(), nil
	}
	raw, err := readAuthoritativeSource(s.canonicalPath, [sha256.Size]byte{})
	if err != nil {
		return s.snapshotLocked(), err
	}
	if sha256.Sum256(raw) == s.contentDigest {
		return s.snapshotLocked(), nil
	}
	if err := s.publishSourceLocked(raw); err != nil {
		return s.snapshotLocked(), err
	}
	return s.regenerateLocked(ctx, regenerate)
}

// ReconcileApprovedSourceChange records an already executed, approved change and
// atomically republishes the authoritative and friendly representations.
func (s *DocumentState) ReconcileApprovedSourceChange(ctx context.Context, change ApprovedSourceChange, regenerate RegenerateNarration) (DocumentSnapshot, error) {
	if change.ProposalID == "" || change.ProposalDigest == "" || change.BaseSourceDigest == "" || !change.ChangesPlan {
		return DocumentSnapshot{}, errors.New("approved source change is incomplete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prior, ok := s.appliedChanges[change.ProposalID]; ok {
		if prior == change.ProposalDigest {
			return s.snapshotLocked(), ErrAlreadyApplied
		}
		return s.snapshotLocked(), ErrStaleRevision
	}
	if hex.EncodeToString(s.contentDigest[:]) != change.BaseSourceDigest {
		return s.snapshotLocked(), ErrStaleRevision
	}
	raw, err := readAuthoritativeSource(s.canonicalPath, [sha256.Size]byte{})
	if err != nil {
		return s.snapshotLocked(), err
	}
	if sha256.Sum256(raw) == s.contentDigest {
		return s.snapshotLocked(), ErrStaleRevision
	}
	s.appliedChanges[change.ProposalID] = change.ProposalDigest
	if err := s.publishSourceLocked(raw); err != nil {
		return s.snapshotLocked(), err
	}
	return s.regenerateLocked(ctx, regenerate)
}

func (s *DocumentState) RetryFriendlyNarration(ctx context.Context, sourceDigest string, regenerate RegenerateNarration) (DocumentSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.friendlyStale {
		return s.snapshotLocked(), nil
	}
	if sourceDigest != hex.EncodeToString(s.contentDigest[:]) {
		return s.snapshotLocked(), ErrStaleRevision
	}
	raw, err := readAuthoritativeSource(s.canonicalPath, s.contentDigest)
	if err != nil {
		return s.snapshotLocked(), err
	}
	if !bytes.Equal(raw, s.rawSource) {
		return s.snapshotLocked(), ErrStaleRevision
	}
	return s.regenerateLocked(ctx, regenerate)
}

func (s *DocumentState) regenerateLocked(ctx context.Context, regenerate RegenerateNarration) (DocumentSnapshot, error) {
	if regenerate == nil {
		s.friendlyStale = true
		return s.snapshotLocked(), ErrFriendlyStale
	}
	sources := orderedSections(s.sections)
	generated, err := regenerate(ctx, string(s.rawSource), sources)
	if err == nil {
		err = narration.ValidateSourceMappings(generated, sources)
	}
	if err != nil {
		s.friendlyStale = true
		return s.snapshotLocked(), fmt.Errorf("%w: %v", ErrFriendlyStale, err)
	}
	friendlyRevision, err := opaqueRevision()
	if err != nil {
		s.friendlyStale = true
		return s.snapshotLocked(), err
	}
	s.narration = generated
	s.friendlyRevision = friendlyRevision
	s.friendlyStale = false
	return s.snapshotLocked(), nil
}

func (s *DocumentState) publishSourceLocked(raw []byte) error {
	sections := narration.SplitMarkdownSections(string(raw))
	byID := make(map[string]narration.SourceSection, len(sections))
	for _, section := range sections {
		byID[section.ID] = section
	}
	revision, err := opaqueRevision()
	if err != nil {
		return err
	}
	s.rawSource = append([]byte(nil), raw...)
	s.contentDigest = sha256.Sum256(raw)
	s.sections = byID
	s.revision = revision
	s.friendlyStale = true
	return nil
}

func (s *DocumentState) snapshotLocked() DocumentSnapshot {
	return DocumentSnapshot{
		Source: string(s.rawSource), SourceDigest: hex.EncodeToString(s.contentDigest[:]),
		Revision: s.revision, FriendlyRevision: s.friendlyRevision, FriendlyStale: s.friendlyStale,
		Sections: orderedSections(s.sections), Narration: s.narration,
	}
}

func orderedSections(sections map[string]narration.SourceSection) []narration.SourceSection {
	result := make([]narration.SourceSection, 0, len(sections))
	for _, section := range sections {
		result = append(result, section)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StartByte < result[j].StartByte })
	return result
}

func readAuthoritativeSource(path string, expected [sha256.Size]byte) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrSourceUnavailable
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrSourceUnavailable
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxDocumentBytes {
		return nil, ErrSourceUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxDocumentBytes+1))
	if err != nil || len(raw) > MaxDocumentBytes || strings.TrimSpace(string(raw)) == "" {
		return nil, ErrSourceUnavailable
	}
	if expected != ([sha256.Size]byte{}) && sha256.Sum256(raw) != expected {
		return nil, ErrStaleRevision
	}
	return raw, nil
}

func (s *DocumentState) Revision() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revision
}

func (s *DocumentState) FriendlyRevision() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.friendlyRevision
}

func (s *DocumentState) VerifyCurrentSource() error {
	s.mu.RLock()
	path := s.canonicalPath
	digest := s.contentDigest
	sourceLength := len(s.rawSource)
	s.mu.RUnlock()
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ErrSourceUnavailable
	}
	if info.Size() != int64(sourceLength) {
		return ErrStaleRevision
	}
	file, err := os.Open(path)
	if err != nil {
		return ErrSourceUnavailable
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, int64(sourceLength+1)))
	if err != nil {
		return ErrSourceUnavailable
	}
	if sha256.Sum256(raw) != digest {
		return ErrStaleRevision
	}
	return nil
}

func (s *DocumentState) ResolveOriginalSelection(request OriginalSelectionRequest) (ResolvedOriginalSelection, error) {
	if err := s.VerifyCurrentSource(); err != nil {
		return ResolvedOriginalSelection{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if request.Revision != s.revision {
		return ResolvedOriginalSelection{}, ErrStaleRevision
	}
	if err := validateSelection(request.Quote, request.StartOffset, request.EndOffset); err != nil {
		return ResolvedOriginalSelection{}, err
	}
	section, ok := s.sections[request.SectionID]
	if !ok || request.EndOffset > len(section.Markdown) {
		return ResolvedOriginalSelection{}, ErrInvalidSelection
	}
	if section.Markdown[request.StartOffset:request.EndOffset] != request.Quote {
		return ResolvedOriginalSelection{}, ErrAmbiguousSelection
	}
	startByte := section.StartByte + request.StartOffset
	endByte := section.StartByte + request.EndOffset
	return ResolvedOriginalSelection{
		Representation: "original",
		Revision:       s.revision,
		SectionID:      request.SectionID,
		Quote:          request.Quote,
		StartByte:      startByte,
		EndByte:        endByte,
		StartLine:      sourceLineAt(s.rawSource, startByte),
		EndLine:        sourceLineAt(s.rawSource, endByte-1),
	}, nil
}

func (s *DocumentState) LocateOriginalSelection(revision, sectionID, quote string) (ResolvedOriginalSelection, error) {
	s.mu.RLock()
	section, ok := s.sections[sectionID]
	s.mu.RUnlock()
	if !ok || strings.TrimSpace(quote) == "" {
		return ResolvedOriginalSelection{}, ErrInvalidSelection
	}
	start := strings.Index(section.Markdown, quote)
	if start < 0 || strings.Index(section.Markdown[start+len(quote):], quote) >= 0 {
		return ResolvedOriginalSelection{}, ErrAmbiguousSelection
	}
	return s.ResolveOriginalSelection(OriginalSelectionRequest{
		Revision: revision, SectionID: sectionID, Quote: quote,
		StartOffset: start, EndOffset: start + len(quote),
	})
}

func (s *DocumentState) ResolveFriendlySelection(request FriendlySelectionRequest) (ResolvedFriendlySelection, error) {
	if err := s.VerifyCurrentSource(); err != nil {
		return ResolvedFriendlySelection{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if request.Revision != s.friendlyRevision {
		return ResolvedFriendlySelection{}, ErrStaleRevision
	}
	if err := validateSelection(request.Quote, request.StartOffset, request.EndOffset); err != nil {
		return ResolvedFriendlySelection{}, err
	}
	for _, section := range s.narration.Sections {
		if section.ID != request.SectionID {
			continue
		}
		if request.BlockIndex < 0 || request.BlockIndex >= len(section.Sentences) {
			return ResolvedFriendlySelection{}, ErrInvalidSelection
		}
		block := section.Sentences[request.BlockIndex]
		if request.EndOffset > len(block) {
			return ResolvedFriendlySelection{}, ErrInvalidSelection
		}
		if block[request.StartOffset:request.EndOffset] != request.Quote {
			return ResolvedFriendlySelection{}, ErrAmbiguousSelection
		}
		return ResolvedFriendlySelection{
			Representation:            "friendly",
			Revision:                  s.friendlyRevision,
			SectionID:                 section.ID,
			BlockIndex:                request.BlockIndex,
			Quote:                     request.Quote,
			CandidateSourceSectionIDs: append([]string(nil), section.SourceSectionIDs...),
		}, nil
	}
	return ResolvedFriendlySelection{}, ErrInvalidSelection
}

func (s *DocumentState) LocateFriendlySelection(revision, sectionID, quote string) (ResolvedFriendlySelection, error) {
	if err := s.VerifyCurrentSource(); err != nil {
		return ResolvedFriendlySelection{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if revision != s.friendlyRevision || strings.TrimSpace(quote) == "" {
		return ResolvedFriendlySelection{}, ErrStaleRevision
	}
	for _, section := range s.narration.Sections {
		if section.ID != sectionID {
			continue
		}
		renderedBlocks := []string{
			section.Heading,
			strings.Join(section.Sentences, ""),
		}
		if section.RecallQuestion != "" {
			renderedBlocks = append(renderedBlocks, "Pause and recall: "+section.RecallQuestion)
		}
		matches := 0
		for _, text := range renderedBlocks {
			matches += strings.Count(text, quote)
		}
		if matches != 1 {
			return ResolvedFriendlySelection{}, ErrAmbiguousSelection
		}
		return ResolvedFriendlySelection{
			Representation:            "friendly",
			Revision:                  s.friendlyRevision,
			SectionID:                 section.ID,
			BlockIndex:                -1,
			Quote:                     quote,
			CandidateSourceSectionIDs: append([]string(nil), section.SourceSectionIDs...),
		}, nil
	}
	return ResolvedFriendlySelection{}, ErrInvalidSelection
}

func validateSelection(quote string, start, end int) error {
	if strings.TrimSpace(quote) == "" || len(quote) > maxSelectionBytes || start < 0 || end <= start || end-start != len(quote) {
		return ErrInvalidSelection
	}
	return nil
}

func sourceLineAt(source []byte, offset int) int {
	line := 1
	for _, value := range source[:offset] {
		if value == '\n' {
			line++
		}
	}
	return line
}

func opaqueRevision() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("creating document revision: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
