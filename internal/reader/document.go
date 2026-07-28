package reader

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/taylorwiebe/planreader/internal/narration"
)

const maxSelectionBytes = 64 << 10

var (
	ErrInvalidSelection   = errors.New("invalid selection")
	ErrAmbiguousSelection = errors.New("selection does not match its anchor")
	ErrStaleRevision      = errors.New("document revision is stale")
	ErrSourceUnavailable  = errors.New("authoritative source is unavailable")
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
	}, nil
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
	s.mu.RUnlock()
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ErrSourceUnavailable
	}
	if info.Size() != int64(len(s.rawSource)) {
		return ErrStaleRevision
	}
	file, err := os.Open(path)
	if err != nil {
		return ErrSourceUnavailable
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, int64(len(s.rawSource)+1)))
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
		matches := 0
		for _, text := range append([]string{section.Heading}, section.Sentences...) {
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
