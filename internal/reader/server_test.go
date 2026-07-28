package reader

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/taylorwiebe/planreader/internal/agentbridge"
	"github.com/taylorwiebe/planreader/internal/narration"
)

func TestReaderDataRefreshesAtomicallyFromDocumentState(t *testing.T) {
	state := testDocumentState(t, "# One\n\nAlpha.\n", friendlyForSource("Alpha."))
	handler := newReaderHandler(ReaderDocument{FileName: "plan.md", State: state}, "token")
	before := readReaderDocument(t, handler, "/reader/token/data.json")
	_, err := state.ApplyApprovedSourceChange(context.Background(), ApprovedSourceChange{
		ProposalID: "proposal-1", ProposalDigest: "digest-1", BaseSourceDigest: state.SourceDigest(), ChangesPlan: true,
	}, func(context.Context) error {
		return os.WriteFile(state.canonicalPath, []byte("# One\n\nChanged.\n"), 0o600)
	}, func(context.Context, string, []narration.SourceSection) (narration.Narration, error) {
		return friendlyForSource("Changed."), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after := readReaderDocument(t, handler, "/reader/token/data.json")
	if after.DocumentRevision == before.DocumentRevision || after.FriendlyRevision == before.FriendlyRevision {
		t.Fatalf("reader revisions did not refresh: before=%#v after=%#v", before, after)
	}
	if len(after.Sources) != 1 || !strings.Contains(after.Sources[0].HTML, "Changed.") ||
		after.Narration.Sections[0].Sentences[0] != "Changed." || after.FriendlyStale {
		t.Fatalf("refreshed reader = %#v", after)
	}
}

func readReaderDocument(t *testing.T, handler http.Handler, path string) ReaderDocument {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d", path, recorder.Code)
	}
	var document ReaderDocument
	if err := json.NewDecoder(recorder.Body).Decode(&document); err != nil {
		t.Fatal(err)
	}
	return document
}

func TestReaderSpeechReadsHeadingsAndAvoidsRepeatedListPrefixes(t *testing.T) {
	script, err := fs.ReadFile(webFiles, "web/reader.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(script)
	if strings.Contains(javascript, "Listen for: ${item.textContent}") {
		t.Fatal("orientation speech repeats the 'Listen for' prefix for every item")
	}
	if strings.Contains(javascript, "`${title}: ${item.textContent}`") {
		t.Fatal("recap speech repeats the category title for every list item")
	}
	for _, expected := range []string{
		`addSpeakable(heading, heading.textContent)`,
		`addSpeakable(sectionHeading, section.heading`,
		`addSpeakable(endHeading, endHeading.textContent)`,
		`addSpeakable(groupHeading, title)`,
		`addSpeakable(item, item.textContent)`,
		`const itemList = list(items)`,
	} {
		if !strings.Contains(javascript, expected) {
			t.Fatalf("reader speech does not preserve heading and list order; missing %q", expected)
		}
	}
	for _, expected := range []string{
		"selectedVoiceURI: null",
		"state.selectedVoiceURI = elements.voice.value",
		"voice.voiceURI === state.selectedVoiceURI",
	} {
		if !strings.Contains(javascript, expected) {
			t.Fatalf("reader JavaScript does not preserve voice selection; missing %q", expected)
		}
	}
}

func TestComputerVoiceListRefreshesWhenSwitchingFromKokoro(t *testing.T) {
	script, err := fs.ReadFile(webFiles, "web/reader.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(script)
	start := strings.Index(javascript, "async function start()")
	if start < 0 {
		t.Fatal("reader startup function is missing")
	}
	startup := javascript[start:]
	listener := strings.Index(startup, `speechSynthesis.addEventListener("voiceschanged", loadVoices)`)
	initialLoad := strings.Index(startup, "loadVoices();")
	settingsLoad := strings.Index(startup, "await loadSpeechSettings();")
	if listener < 0 || initialLoad < 0 || settingsLoad < 0 || listener > initialLoad || listener > settingsLoad {
		t.Fatal("the reader can miss browser voices that become available while settings load")
	}
	useSystemStart := strings.Index(javascript, "async function useSystemVoice")
	useSystemEnd := strings.Index(javascript[useSystemStart:], "\n  function formatSize")
	if useSystemStart < 0 || useSystemEnd < 0 || !strings.Contains(javascript[useSystemStart:useSystemStart+useSystemEnd], "loadVoices();") {
		t.Fatal("switching from Kokoro does not refresh the browser voice list")
	}
	if !strings.Contains(javascript, "Loading computer voices…") {
		t.Fatal("an empty browser voice list has no visible loading state")
	}
}

func TestVoiceConfigurationLivesInSettingsAndNewModelsUseTheirDefaultVoice(t *testing.T) {
	markup, err := fs.ReadFile(webFiles, "web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	javascript, err := fs.ReadFile(webFiles, "web/reader.js")
	if err != nil {
		t.Fatal(err)
	}

	html := string(markup)
	settingsStart := strings.Index(html, `id="settings-dialog"`)
	settingsEnd := strings.Index(html, `</dialog>`)
	voiceMenu := strings.Index(html, `id="voice"`)
	playerStart := strings.Index(html, `class="player"`)
	if settingsStart < 0 || settingsEnd < 0 || voiceMenu < settingsStart || voiceMenu > settingsEnd {
		t.Fatal("voice selection is not contained in the speech settings dialog")
	}
	if playerStart < 0 || strings.Contains(html[playerStart:], `id="voice"`) || strings.Contains(html[playerStart:], `id="preview-voice"`) {
		t.Fatal("the playback bar still contains voice configuration")
	}
	if !strings.Contains(string(javascript), `state.modelID === model.id ? state.localVoice : model.default_voice`) {
		t.Fatal("switching Kokoro models does not start with that model's default voice")
	}
	for _, expected := range []string{`model.install_path`, `element("code", "", model.install_path)`, `model.install_path = result.install_path`} {
		if !strings.Contains(string(javascript), expected) {
			t.Fatalf("installed model location is not shown or refreshed; missing %q", expected)
		}
	}
}

func TestReaderPrefetchesLocalAudioAndInvalidatesItWhenSettingsChange(t *testing.T) {
	script, err := fs.ReadFile(webFiles, "web/reader.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(script)
	for _, expected := range []string{
		"localAudio: new Map()",
		"const localAudioLookahead = 6",
		"const existing = state.localAudio.get(key)",
		"warmLocalAudioAhead(index)",
		"for (let offset = 0; offset <= localAudioLookahead; offset += 1)",
		"clearLocalAudio();",
		"warmLocalAudioAhead(0)",
	} {
		if !strings.Contains(javascript, expected) {
			t.Fatalf("reader JavaScript does not prefetch and invalidate local audio; missing %q", expected)
		}
	}
}

func TestLocalAudioContinuesPlayingAfterAQueuedBlockLoads(t *testing.T) {
	script, err := fs.ReadFile(webFiles, "web/reader.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(script)
	for _, expected := range []string{
		"playRequested: false",
		"const continuePlaying = state.playRequested",
		"if (continuePlaying && state.playing && state.playRequested)",
		"state.playRequested = false",
		"await audio.play()",
	} {
		if !strings.Contains(javascript, expected) {
			t.Fatalf("reader does not preserve continuous playback while queued audio loads; missing %q", expected)
		}
	}
}

func TestLocalAudioBuffersTheDocumentStartBeforeInitialPlayback(t *testing.T) {
	script, err := fs.ReadFile(webFiles, "web/reader.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(script)
	for _, expected := range []string{
		"const localStartupBufferSize = 3",
		"const localStartupBufferWait = 2_000",
		"async function waitForLocalStartupBuffer(index)",
		"Promise.race([buffered, timeout])",
		"elements.status.textContent = \"Preparing your reader…\"",
		"speakCurrent(true)",
	} {
		if !strings.Contains(javascript, expected) {
			t.Fatalf("reader does not briefly buffer the document start before initial playback; missing %q", expected)
		}
	}
}

func TestPlaybackStateFollowsAudioAndKeyboardMediaControls(t *testing.T) {
	script, err := fs.ReadFile(webFiles, "web/reader.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(script)
	for _, expected := range []string{
		`audio.addEventListener("play"`,
		`audio.addEventListener("pause"`,
		`navigator.mediaSession.setActionHandler("play"`,
		`navigator.mediaSession.setActionHandler("pause"`,
		`navigator.mediaSession.playbackState`,
		"primedAudio: null",
		"audioContext: null",
		"primeLocalAudio()",
		"state.audioContext.resume()",
		"audio = bufferedAudio(context, buffer)",
	} {
		if !strings.Contains(javascript, expected) {
			t.Fatalf("playback UI is not synchronized with media playback; missing %q", expected)
		}
	}
}

func TestPlaybackToolbarUsesAnIconAndHidesTextStatus(t *testing.T) {
	markup, err := fs.ReadFile(webFiles, "web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	javascript, err := fs.ReadFile(webFiles, "web/reader.js")
	if err != nil {
		t.Fatal(err)
	}
	styles, err := fs.ReadFile(webFiles, "web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markup), `id="play" class="primary icon-button"`) {
		t.Fatal("playback control is not presented as an icon button")
	}
	if strings.Contains(string(markup), `id="stop"`) || strings.Contains(string(javascript), "elements.stop") {
		t.Fatal("the redundant Stop control is still exposed in the player")
	}
	if !strings.Contains(string(markup), `id="status" class="status player-status"`) {
		t.Fatal("toolbar status is not isolated for visually hidden announcements")
	}
	for _, expected := range []string{`"▶"`, `"Ⅱ"`, `elements.play.setAttribute("aria-label"`} {
		if !strings.Contains(string(javascript), expected) {
			t.Fatalf("playback icon state is missing %q", expected)
		}
	}
	if !strings.Contains(string(styles), ".player-status {") || !strings.Contains(string(styles), "clip-path: inset(50%)") {
		t.Fatal("toolbar text status remains visually visible")
	}
}

func TestReaderKeepsKokoroSelectedThroughTransientFailuresAndShowsLoading(t *testing.T) {
	script, err := fs.ReadFile(webFiles, "web/reader.js")
	if err != nil {
		t.Fatal(err)
	}
	markup, err := fs.ReadFile(webFiles, "web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	styles, err := fs.ReadFile(webFiles, "web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(script)
	for _, expected := range []string{
		"localRetryCount < 1",
		"evictLocalAudio(index)",
		"Kokoro paused. Press Play to retry.",
		"elements.play.disabled = waiting",
		`elements.play.setAttribute("aria-busy", String(waiting))`,
		"elements.audioLoading.hidden = !waiting",
	} {
		if !strings.Contains(javascript, expected) {
			t.Fatalf("reader JavaScript is missing local recovery behavior %q", expected)
		}
	}
	if strings.Contains(javascript, "The local voice stopped working") {
		t.Fatal("a transient local speech error still switches to the computer voice")
	}
	if !strings.Contains(string(markup), `role="progressbar"`) {
		t.Fatal("reader does not expose an accessible audio loading indicator")
	}
	playStart := strings.Index(string(markup), `id="play"`)
	if playStart < 0 {
		t.Fatal("reader is missing the play button")
	}
	playEnd := playStart + strings.Index(string(markup)[playStart:], `</button>`)
	loading := strings.Index(string(markup), `id="audio-loading"`)
	if playEnd < playStart || loading < playStart || loading > playEnd {
		t.Fatal("audio loading indicator is not contained in the play button")
	}
	if !strings.Contains(string(styles), ".button-loading::after") {
		t.Fatal("play button loading indicator does not preserve the horizontal moving bar")
	}
}

func TestReaderKeepsNarrationAndSourceInIndependentScrollPanes(t *testing.T) {
	styles, err := fs.ReadFile(webFiles, "web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(styles)
	for _, expected := range []string{
		"height: 100vh",
		"overflow: hidden",
		"flex: 1",
		"overflow-y: auto",
		"overscroll-behavior: contain",
		"padding-bottom: 10rem",
		"scroll-padding-bottom: 10rem",
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("reader layout does not keep both documents in independent scroll panes; missing %q", expected)
		}
	}
}

func TestReaderUsesSimpleDesktopDividerAndMobileSourceOverlay(t *testing.T) {
	styles, err := fs.ReadFile(webFiles, "web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	markup, err := fs.ReadFile(webFiles, "web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	javascript, err := fs.ReadFile(webFiles, "web/reader.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"border-left: 1px solid var(--line)",
		".source-scroll {",
		"overflow-y: auto",
		"flex: 0 0 auto",
		"position: fixed",
		"transform: translateX(105%)",
		".source-pane.visible { transform: translateX(0); visibility: visible; transition-delay: 0s; }",
	} {
		if !strings.Contains(string(styles), expected) {
			t.Fatalf("reader layout is missing %q", expected)
		}
	}
	if !strings.Contains(string(markup), `id="source-close"`) {
		t.Fatal("mobile source overlay has no close control")
	}
	if !strings.Contains(string(markup), `id="source" class="source-scroll"`) {
		t.Fatal("source document does not scroll independently below its fixed heading")
	}
	if !strings.Contains(string(javascript), `elements.sourceClose.addEventListener`) {
		t.Fatal("mobile source overlay close control is not wired")
	}
	if !strings.Contains(string(javascript), `event.key === "Escape"`) {
		t.Fatal("mobile source overlay cannot be dismissed with Escape")
	}
	for _, expected := range []string{
		`elements.sourcePane.contains(event.target)`,
		`elements.settingsDialog.getBoundingClientRect()`,
		`elements.settingsDialog.close()`,
	} {
		if !strings.Contains(string(javascript), expected) {
			t.Fatalf("reader click-away behavior is missing %q", expected)
		}
	}
}

func TestReaderControlsStayUsableOnMobile(t *testing.T) {
	styles, err := fs.ReadFile(webFiles, "web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(styles)
	for _, expected := range []string{
		"min-height: 2.75rem",
		"align-items: flex-start",
		"width: fit-content",
		"grid-template-columns: auto auto",
		"grid-template-columns: repeat(2, minmax(0, 1fr))",
		"grid-template-columns: repeat(4, minmax(0, 1fr))",
		".player-buttons button",
		"flex-direction: column",
		"white-space: nowrap",
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("mobile controls are missing responsive rule %q", expected)
		}
	}
}

func TestRecallPromptUsesAStableBlockCallout(t *testing.T) {
	styles, err := fs.ReadFile(webFiles, "web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(styles)
	for _, expected := range []string{
		".recall {",
		"display: block",
		"border-left: 3px solid var(--accent)",
		".recall.active { background: var(--accent-soft); box-shadow: none; }",
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("recall prompt is missing stable callout styling %q", expected)
		}
	}
}

func TestSpokenRecapItemsKeepVisibleListMarkers(t *testing.T) {
	styles, err := fs.ReadFile(webFiles, "web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(styles)
	for _, expected := range []string{
		"li.sentence { display: list-item; }",
		".recap-card ul { margin: 0; padding-left: 1.5rem; }",
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("spoken recap lists are missing visible marker styling %q", expected)
		}
	}
}

func TestSourceHighlightUsesASubtleReadingCue(t *testing.T) {
	styles, err := fs.ReadFile(webFiles, "web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(styles)
	for _, expected := range []string{
		"border-left: 3px solid transparent",
		"border-left-color: var(--accent)",
		"background: linear-gradient(90deg, var(--source-active)",
		"border-radius: 0 8px 8px 0",
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("source highlight is missing subtle reading cue %q", expected)
		}
	}
}

func TestReaderHandlerRequiresToken(t *testing.T) {
	document := ReaderDocument{
		FileName: "plan.md",
		Narration: narration.Narration{
			Title:    "Plan",
			Sections: []narration.NarrationSection{{ID: "intro", Heading: "Intro", Sentences: []string{"Hello."}}},
		},
	}
	handler := newReaderHandler(document, "secret-token")

	request := httptest.NewRequest(http.MethodGet, "/reader/wrong/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestReaderHandlerServesDocumentData(t *testing.T) {
	document := ReaderDocument{
		FileName: "plan.md",
		Narration: narration.Narration{
			Title:    "Plan",
			Sections: []narration.NarrationSection{{ID: "intro", Heading: "Intro", Sentences: []string{"Hello."}}},
		},
	}
	handler := newReaderHandler(document, "secret-token")

	request := httptest.NewRequest(http.MethodGet, "/reader/secret-token/data.json", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
	var got ReaderDocument
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Narration.Title != "Plan" {
		t.Fatalf("title = %q, want Plan", got.Narration.Title)
	}
	if got.AgentManaged {
		t.Fatal("ordinary reader data incorrectly enables the agent heartbeat")
	}
}

func TestLegacyReaderHasNoConversationEndpointOrAttachmentMetadata(t *testing.T) {
	handler := newReaderHandler(ReaderDocument{
		FileName: "plan.md",
		Narration: narration.Narration{
			Title: "Plan", Sections: []narration.NarrationSection{{ID: "intro", Heading: "Intro", Sentences: []string{"Hello."}}},
		},
	}, "secret-token")

	conversation := httptest.NewRecorder()
	handler.ServeHTTP(conversation, httptest.NewRequest(http.MethodGet, "/reader/secret-token/api/conversation/browser/events", nil))
	if conversation.Code != http.StatusNotFound {
		t.Fatalf("legacy conversation endpoint status = %d, want 404", conversation.Code)
	}

	data := httptest.NewRecorder()
	handler.ServeHTTP(data, httptest.NewRequest(http.MethodGet, "/reader/secret-token/data.json", nil))
	body := data.Body.String()
	for _, forbidden := range []string{"task_secret", "attachment_id", "CODEX_THREAD_ID", "canonical_path", "raw_source"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("legacy browser payload leaked %q: %s", forbidden, body)
		}
	}
}

func TestAttachedBrowserPayloadOmitsTaskCredentialsAndServerState(t *testing.T) {
	t.Setenv("PLANREADER_PRIVATE_TEST_VALUE", "environment-secret")
	state := testDocumentState(t, "# One\n\nVisible source.\n", friendlyForSource("Visible source."))
	lifecycle := newAgentLifecycle(func() {}, time.Minute, time.Hour)
	defer lifecycle.Close()
	broker := agentbridge.NewBroker("attachment-private", "task-private")
	handler := newReaderHandlerWithBridge(ReaderDocument{
		FileName: "plan.md", State: state, CanEdit: true,
	}, "browser-token", nil, lifecycle, agentbridge.NewHTTPHandler(broker, ""))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/reader/browser-token/data.json", nil))
	body := response.Body.String()
	for _, forbidden := range []string{
		"task-private", "attachment-private", "environment-secret", state.canonicalPath,
		"canonical_path", "raw_source", "content_digest", "PLANREADER_PRIVATE_TEST_VALUE",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("attached browser payload leaked %q: %s", forbidden, body)
		}
	}
}

func TestAgentManagedReaderDataEnablesBrowserHeartbeat(t *testing.T) {
	lifecycle := newAgentLifecycle(func() {}, time.Minute, time.Hour)
	defer lifecycle.Close()
	handler := newReaderHandlerWithSpeechAndLifecycle(ReaderDocument{
		FileName:  "plan.md",
		Narration: narration.Narration{Title: "Plan"},
	}, "secret-token", nil, lifecycle)

	request := httptest.NewRequest(http.MethodGet, "/reader/secret-token/data.json", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	var got ReaderDocument
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.AgentManaged {
		t.Fatal("agent-managed reader data does not enable the browser heartbeat")
	}
}

func TestAgentLifecycleShutdownRequiresPost(t *testing.T) {
	shutdown := make(chan struct{}, 1)
	handler := newReaderHandlerWithSpeechAndLifecycle(ReaderDocument{
		FileName:  "plan.md",
		Narration: narration.Narration{Title: "Plan", Sections: []narration.NarrationSection{{ID: "intro", Heading: "Intro", Sentences: []string{"Hello."}}}},
	}, "secret-token", nil, newAgentLifecycle(func() { shutdown <- struct{}{} }, time.Minute, time.Hour))

	getRequest := httptest.NewRequest(http.MethodGet, "/reader/secret-token/api/agent/shutdown", nil)
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want %d", getResponse.Code, http.StatusMethodNotAllowed)
	}

	postRequest := httptest.NewRequest(http.MethodPost, "/reader/secret-token/api/agent/shutdown", nil)
	postResponse := httptest.NewRecorder()
	handler.ServeHTTP(postResponse, postRequest)
	if postResponse.Code != http.StatusNoContent {
		t.Fatalf("POST status = %d, want %d", postResponse.Code, http.StatusNoContent)
	}
	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback was not called")
	}
}

func TestReaderRoutesConversationWithoutExposingTaskSecret(t *testing.T) {
	broker := agentbridge.NewBroker("attachment-1", "task-secret")
	handler := newReaderHandlerWithBridge(ReaderDocument{
		FileName: "plan.md", Narration: narration.Narration{Title: "Plan"},
	}, "browser-token", nil, nil, agentbridge.NewHTTPHandler(broker, ""))

	turn := `{"id":"turn-1","controller_id":"controller-1","text":"question"}`
	request := httptest.NewRequest(http.MethodPost, "/reader/browser-token/api/conversation/browser/turns", strings.NewReader(turn))
	request.Header.Set("Origin", "http://example.com")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("browser turn status = %d, body = %s", response.Code, response.Body.String())
	}

	taskRequest := httptest.NewRequest(http.MethodGet, "/reader/browser-token/api/conversation/task/turns/wait?attachment_id=attachment-1", nil)
	taskRequest.Header.Set("X-Planreader-Task-Secret", "browser-token")
	taskResponse := httptest.NewRecorder()
	handler.ServeHTTP(taskResponse, taskRequest)
	if taskResponse.Code != http.StatusUnauthorized {
		t.Fatalf("browser token reached task mailbox: %d", taskResponse.Code)
	}
}

func TestAgentLifecycleWaitsForMissingBrowserHeartbeat(t *testing.T) {
	shutdown := make(chan struct{}, 1)
	lifecycle := newAgentLifecycle(func() { shutdown <- struct{}{} }, 20*time.Millisecond, time.Hour)
	defer lifecycle.Close()

	lifecycle.Heartbeat("tab-one")
	time.Sleep(10 * time.Millisecond)
	lifecycle.Heartbeat("tab-one")
	select {
	case <-shutdown:
		t.Fatal("active heartbeat shut the reader down")
	case <-time.After(15 * time.Millisecond):
	}
	select {
	case <-shutdown:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("missing heartbeat did not shut the reader down")
	}
}

func TestAgentManagedReaderScriptReportsBrowserLifecycle(t *testing.T) {
	script, err := fs.ReadFile(webFiles, "web/reader.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"readerDocument.agent_managed", "api/agent/heartbeat", "pagehide", `request("DELETE", true)`} {
		if !strings.Contains(string(script), expected) {
			t.Fatalf("reader script is missing agent lifecycle behavior %q", expected)
		}
	}
}

func TestRenderSourceSectionsEscapesRawHTML(t *testing.T) {
	sections, err := RenderSourceSections([]narration.SourceSection{{
		ID:       "source-0",
		Heading:  "Unsafe",
		Markdown: "# Unsafe\n<script>alert('no')</script>",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sections[0].HTML, "<script>") {
		t.Fatalf("rendered HTML contains an executable script: %s", sections[0].HTML)
	}
}

func TestRenderSourceSectionsRendersTables(t *testing.T) {
	sections, err := RenderSourceSections([]narration.SourceSection{{
		ID:       "source-0",
		Heading:  "Table",
		Markdown: "| Item | Result |\n|---|---|\n| One | Good |",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sections[0].HTML, "<table>") {
		t.Fatalf("rendered HTML does not contain a table: %s", sections[0].HTML)
	}
}

func TestAttachedConversationAssetsCoverDockAndSafeSelection(t *testing.T) {
	markup, err := fs.ReadFile(webFiles, "web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	styles, err := fs.ReadFile(webFiles, "web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := fs.ReadFile(webFiles, "web/conversation.js")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := fs.ReadFile(webFiles, "web/reader.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`id="conversation-restore"`, `id="conversation-panel"`, `role="log"`, `id="selection-chip"`, `role="alert"`} {
		if !strings.Contains(string(markup), expected) {
			t.Errorf("conversation markup missing %q", expected)
		}
	}
	for _, expected := range []string{".conversation-compact", ".conversation-expanded", "@media"} {
		if !strings.Contains(string(styles), expected) {
			t.Errorf("conversation styles missing %q", expected)
		}
	}
	for _, expected := range []string{"sessionStorage", "selectionchange", `representation === "original"`,
		"observer-only", "Approval required", "Provider authorization", "Reconnecting", "textContent"} {
		if !strings.Contains(string(conversation), expected) {
			t.Errorf("conversation behavior missing %q", expected)
		}
	}
	if !strings.Contains(string(reader), `speechPointerDragged || (selection && !selection.isCollapsed)`) {
		t.Fatal("pointer text selection can still trigger speech")
	}
	for _, expected := range []string{"pendingPointerPlayback", `event.detail > 1`, `node.addEventListener("dblclick"`} {
		if !strings.Contains(string(reader), expected) {
			t.Fatalf("double-click text selection can still trigger speech; missing %q", expected)
		}
	}
}

func TestReaderResolvesSelectionsWithoutExposingSource(t *testing.T) {
	source := "# One\n\nAuthoritative passage.\n"
	sections := narration.SplitMarkdownSections(source)
	friendly := narration.Narration{Title: "Friendly", Sections: []narration.NarrationSection{{
		ID: "friendly-1", Heading: "One", Sentences: []string{"Clear explanation."},
		SourceSectionIDs: []string{sections[0].ID},
	}}}
	state := testDocumentState(t, source, friendly)
	broker := agentbridge.NewBroker("attachment-1", "task-secret")
	handler := newReaderHandlerWithBridge(ReaderDocument{State: state}, "browser-token", nil, nil, agentbridge.NewHTTPHandler(broker, ""))

	for _, body := range []string{
		`{"representation":"original","revision":"` + state.Revision() + `","section_id":"` + sections[0].ID + `","block_index":0,"quote":"Authoritative passage.","start_offset":0,"end_offset":22}`,
		`{"representation":"friendly","revision":"` + state.FriendlyRevision() + `","section_id":"friendly-1","block_index":0,"quote":"Clear explanation.","start_offset":0,"end_offset":18}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/reader/browser-token/api/conversation/browser/selection", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "http://example.com")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("selection status = %d, body = %s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), source) {
			t.Fatal("selection response exposed the authoritative document")
		}
	}
}
