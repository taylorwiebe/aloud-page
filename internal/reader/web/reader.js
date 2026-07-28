(() => {
  "use strict";

  const elements = {
    title: document.querySelector("#title"),
    filename: document.querySelector("#filename"),
    orientation: document.querySelector("#orientation"),
    narration: document.querySelector("#narration"),
    recap: document.querySelector("#recap"),
    source: document.querySelector("#source"),
    sourcePane: document.querySelector("#source-pane"),
    sourceToggle: document.querySelector("#source-toggle"),
    sourceClose: document.querySelector("#source-close"),
    play: document.querySelector("#play"),
    previous: document.querySelector("#previous"),
    next: document.querySelector("#next"),
    voice: document.querySelector("#voice"),
    rate: document.querySelector("#rate"),
    rateValue: document.querySelector("#rate-value"),
    status: document.querySelector("#status"),
    audioLoading: document.querySelector("#audio-loading"),
    playIcon: document.querySelector("#play-icon"),
    speechSource: document.querySelector("#speech-source"),
    speechSettings: document.querySelector("#speech-settings"),
    settingsDialog: document.querySelector("#settings-dialog"),
    modelCatalog: document.querySelector("#model-catalog"),
    settingsStatus: document.querySelector("#settings-status"),
    fallbackNotice: document.querySelector("#fallback-notice"),
    useSystem: document.querySelector("#use-system"),
    previewVoice: document.querySelector("#preview-voice"),
  };

  const state = {
    sentences: [],
    index: 0,
    playing: false,
    paused: false,
    playRequested: false,
    utterance: null,
    voices: [],
    selectedVoice: null,
    selectedVoiceURI: null,
    playbackID: 0,
    activeNodes: [],
    engine: "system",
    modelID: "",
    localVoice: "",
    models: [],
    audio: null,
    primedAudio: null,
    audioContext: null,
    localAudio: new Map(),
    localAudioControllers: new Map(),
    localRetryIndex: -1,
    localRetryCount: 0,
  };
  const localAudioLookahead = 6;
  const localStartupBufferSize = 3;
  const localStartupBufferWait = 2_000;
  let speechPointerStart = null;
  let speechPointerDragged = false;
  let pendingPointerPlayback = 0;

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text) node.textContent = text;
    return node;
  }

  function list(items) {
    const ul = element("ul");
    for (const item of items || []) ul.append(element("li", "", item));
    return ul;
  }

  function renderOrientation(narration) {
    const heading = element("h2", "", "Why this matters");
    const why = element("p", "sentence", narration.why_it_matters);
    addSpeakable(heading, heading.textContent);
    addSpeakable(why, narration.why_it_matters);
    elements.orientation.append(heading, why);
    if (narration.what_to_listen_for?.length) {
      const items = list(narration.what_to_listen_for);
      const listenHeading = element("h2", "", "What to listen for");
      addSpeakable(listenHeading, listenHeading.textContent);
      [...items.children].forEach((item) => addSpeakable(item, item.textContent));
      elements.orientation.append(listenHeading, items);
    }
    if (narration.estimated_minutes) {
      const duration = element("p", "sentence", `About ${narration.estimated_minutes} minutes.`);
      addSpeakable(duration, `This briefing will take about ${narration.estimated_minutes} minutes.`);
      elements.orientation.append(duration);
    }
  }

  function addSpeakable(node, text, sourceIDs = []) {
    const index = state.sentences.length;
    node.classList.add("sentence");
    node.tabIndex = 0;
    node.dataset.speakableIndex = String(index);
    node.addEventListener("pointerdown", (event) => {
      speechPointerStart = { x: event.clientX, y: event.clientY };
      speechPointerDragged = false;
    });
    node.addEventListener("pointermove", (event) => {
      if (!speechPointerStart) return;
      if (Math.hypot(event.clientX - speechPointerStart.x, event.clientY - speechPointerStart.y) > 5) {
        speechPointerDragged = true;
      }
    });
    node.addEventListener("click", (event) => {
      const selection = window.getSelection();
      const suppressPlayback = event.detail !== 0 && (speechPointerDragged || (selection && !selection.isCollapsed));
      speechPointerStart = null;
      speechPointerDragged = false;
      if (pendingPointerPlayback) {
        window.clearTimeout(pendingPointerPlayback);
        pendingPointerPlayback = 0;
      }
      if (suppressPlayback || event.detail > 1) return;
      if (event.detail !== 0) {
        pendingPointerPlayback = window.setTimeout(() => {
          pendingPointerPlayback = 0;
          const currentSelection = window.getSelection();
          if (!currentSelection || currentSelection.isCollapsed) playFrom(index);
        }, 250);
        return;
      }
      playFrom(index);
    });
    node.addEventListener("dblclick", () => {
      if (pendingPointerPlayback) window.clearTimeout(pendingPointerPlayback);
      pendingPointerPlayback = 0;
    });
    node.addEventListener("keydown", (event) => {
      const selection = window.getSelection();
      if ((event.key === "Enter" || event.key === " ") && (!selection || selection.isCollapsed)) playFrom(index);
    });
    state.sentences.push({ text, node, sourceIDs });
  }

  function renderNarration(narration) {
    narration.sections.forEach((section) => {
      const article = element("article", "narration-section");
      article.id = `narration-${section.id}`;
      article.dataset.friendlySection = section.id;
      const sectionHeading = element("h2", "", section.heading);
      addSpeakable(sectionHeading, section.heading, section.source_section_ids || []);
      article.append(sectionHeading);
      const paragraph = element("p", "narration-copy");
      section.sentences.forEach((sentence, blockIndex) => {
        const span = element("span", "sentence", sentence);
        addSpeakable(span, sentence, section.source_section_ids || []);
        span.dataset.friendlySection = section.id;
        span.dataset.blockIndex = String(blockIndex);
        paragraph.append(span);
      });
      article.append(paragraph);
      if (section.recall_question) {
        const recall = element("p", "recall sentence", `Pause and recall: ${section.recall_question}`);
        addSpeakable(recall, `Pause and recall. ${section.recall_question}`, section.source_section_ids || []);
        article.append(recall);
      }
      elements.narration.append(article);
    });
  }

  function renderRecap(narration) {
    const groups = [
      ["Remember", narration.remember],
      ["Decisions", narration.decisions],
      ["Actions", narration.actions],
      ["Verify in the source", narration.verify],
    ].filter(([, items]) => items?.length);
    if (!groups.length) return;
    const endHeading = element("h2", "", "At the end");
    addSpeakable(endHeading, endHeading.textContent);
    elements.recap.append(endHeading);
    const grid = element("div", "recap-grid");
    groups.forEach(([title, items]) => {
      const card = element("section", "recap-card");
      const groupHeading = element("h3", "", title);
      addSpeakable(groupHeading, title);
      const itemList = list(items);
      [...itemList.children].forEach((item) => {
        addSpeakable(item, item.textContent);
      });
      card.append(groupHeading, itemList);
      grid.append(card);
    });
    elements.recap.append(grid);
  }

  function renderSource(sources) {
    for (const source of sources) {
      const section = element("section", "source-section");
      section.id = source.id;
      section.innerHTML = source.html;
      elements.source.append(section);
    }
  }

  function highlight(index) {
    state.activeNodes.forEach((node) => node.classList.remove("active"));
    state.activeNodes = [];
    const current = state.sentences[index];
    if (!current) return;
    current.node.classList.add("active");
    state.activeNodes.push(current.node);
    current.node.scrollIntoView({ behavior: "smooth", block: "center" });
    current.sourceIDs.forEach((id, sourceIndex) => {
      const source = document.getElementById(id);
      if (!source) return;
      source.classList.add("active");
      state.activeNodes.push(source);
      if (sourceIndex === 0) source.scrollIntoView({ behavior: "smooth", block: "center" });
    });
    updateAudioLoading();
  }

  function selectedVoice() {
    return state.voices.find((voice) => voice.voiceURI === state.selectedVoiceURI) || state.selectedVoice;
  }

  async function savePreferences() {
    const response = await fetch("api/preferences", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        engine: state.engine,
        model_id: state.modelID,
        voice: state.localVoice,
        system_voice: state.selectedVoiceURI || "",
        rate: Number(elements.rate.value),
      }),
    });
    if (!response.ok) throw new Error((await response.json()).error || "Could not save speech settings");
  }

  function configureVoiceMenu() {
    elements.voice.replaceChildren();
    if (state.engine === "local") {
      const model = state.models.find((item) => item.id === state.modelID && item.installed);
      if (!model) return useSystemVoice("The selected voice pack is missing, so Planreader switched to a computer voice.");
      model.voices.forEach((name) => {
        const option = element("option", "", name);
        option.value = name;
        elements.voice.append(option);
      });
      state.localVoice = model.voices.includes(state.localVoice) ? state.localVoice : (model.default_voice || model.voices[0]);
      elements.voice.value = state.localVoice;
      elements.speechSource.textContent = model.name;
      return;
    }
    state.voices.forEach((voice) => {
      const option = element("option", "", `${voice.name}${voice.localService ? " — local" : ""}`);
      option.value = voice.voiceURI;
      elements.voice.append(option);
    });
    if (!state.voices.length) {
      const option = element("option", "", "Loading computer voices…");
      option.disabled = true;
      elements.voice.append(option);
    }
    if (state.selectedVoiceURI) elements.voice.value = state.selectedVoiceURI;
    elements.speechSource.textContent = "Computer voice";
  }

  function localAudioKey(index) {
    return JSON.stringify([index, state.modelID, state.localVoice, Number(elements.rate.value), state.sentences[index]?.text || ""]);
  }

  function updateAudioLoading() {
    const waiting = state.engine === "local" && !state.audio && state.playing && !state.paused;
    elements.play.disabled = waiting;
    elements.play.setAttribute("aria-busy", String(waiting));
    elements.audioLoading.hidden = !waiting;
    const label = waiting ? "Preparing audio" : (!state.playing ? "Play" : (state.paused ? "Resume" : "Pause"));
    elements.play.setAttribute("aria-label", label);
    elements.play.title = label;
  }

  function syncPlaybackUI() {
    const label = !state.playing ? "Play" : (state.paused ? "Resume" : "Pause");
    elements.playIcon.textContent = state.playing && !state.paused ? "Ⅱ" : "▶";
    elements.play.setAttribute("aria-label", label);
    elements.play.title = label;
    if ("mediaSession" in navigator) {
      navigator.mediaSession.playbackState = !state.playing ? "none" : (state.paused ? "paused" : "playing");
    }
    updateAudioLoading();
  }

  function evictLocalAudio(index) {
    const key = localAudioKey(index);
    state.localAudioControllers.get(key)?.abort();
    state.localAudioControllers.delete(key);
    state.localAudio.delete(key);
    updateAudioLoading();
  }

  function clearLocalAudio() {
    state.localAudioControllers.forEach((controller) => controller.abort());
    state.localAudioControllers.clear();
    state.localAudio.clear();
    updateAudioLoading();
  }

  function prepareLocalAudio(index) {
    if (state.engine !== "local" || !state.sentences[index]) return null;
    const key = localAudioKey(index);
    const existing = state.localAudio.get(key);
    if (existing) return existing;
    const controller = new AbortController();
    state.localAudioControllers.set(key, controller);
    updateAudioLoading();
    const pending = fetch("api/synthesize", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text: state.sentences[index].text, model_id: state.modelID, voice: state.localVoice, rate: Number(elements.rate.value) }),
        signal: controller.signal,
      })
      .then(async (response) => {
        if (!response.ok) throw new Error((await response.json()).error || "Local speech failed");
        return response.json();
      })
      .catch((error) => {
        state.localAudio.delete(key);
        throw error;
      })
      .finally(() => {
        state.localAudioControllers.delete(key);
        updateAudioLoading();
      });
    state.localAudio.set(key, pending);
    return pending;
  }

  function warmLocalAudio(index = state.index) {
    const pending = prepareLocalAudio(index);
    if (pending) pending.catch(() => {});
  }

  function warmLocalAudioAhead(index = state.index) {
    for (let offset = 0; offset <= localAudioLookahead; offset += 1) {
      warmLocalAudio(index + offset);
    }
  }

  async function waitForLocalStartupBuffer(index) {
    if (state.engine !== "local" || index !== 0) return;
    const pending = [];
    for (let offset = 0; offset < localStartupBufferSize; offset += 1) {
      const audio = prepareLocalAudio(index + offset);
      if (audio) pending.push(audio);
    }
    if (!pending.length) return;
    elements.status.textContent = "Preparing your reader…";
    let timeoutID;
    const buffered = Promise.allSettled(pending);
    const timeout = new Promise((resolve) => {
      timeoutID = window.setTimeout(resolve, localStartupBufferWait);
    });
    await Promise.race([buffered, timeout]);
    window.clearTimeout(timeoutID);
  }

  function releasePrimedAudio() {
    state.primedAudio = null;
  }

  function primeLocalAudio() {
    if (state.engine !== "local") return;
    const AudioContextClass = window.AudioContext || window.webkitAudioContext;
    if (!AudioContextClass) return;
    state.audioContext ||= new AudioContextClass();
    state.primedAudio = state.audioContext;
    state.audioContext.resume().catch(() => {});
  }

  function bufferedAudio(context, buffer) {
    const listeners = { play: [], pause: [] };
    let source = null;
    let offset = 0;
    let startedAt = 0;
    let stopping = false;
    const audio = {
      ended: false,
      onended: null,
      onerror: null,
      addEventListener(type, listener) {
        if (listeners[type]) listeners[type].push(listener);
      },
      async play() {
        await context.resume();
        if (source || audio.ended) return;
        source = context.createBufferSource();
        const currentSource = source;
        currentSource.buffer = buffer;
        currentSource.connect(context.destination);
        currentSource.onended = () => {
          if (source === currentSource) source = null;
          if (stopping) {
            stopping = false;
            return;
          }
          audio.ended = true;
          offset = 0;
          audio.onended?.();
        };
        startedAt = context.currentTime;
        currentSource.start(0, offset);
        listeners.play.forEach((listener) => listener());
      },
      pause() {
        if (!source) return;
        offset = Math.min(buffer.duration, offset + context.currentTime - startedAt);
        stopping = true;
        const currentSource = source;
        source = null;
        currentSource.stop();
        listeners.pause.forEach((listener) => listener());
      },
      removeAttribute() {
        audio.pause();
        audio.ended = true;
        offset = 0;
      },
    };
    return audio;
  }

  async function speakLocal(playbackID, waitForStartupBuffer = false) {
    const index = state.index;
    const continuePlaying = state.playRequested;
    const resultPending = prepareLocalAudio(index);
    if (waitForStartupBuffer) await waitForLocalStartupBuffer(index);
    const result = await resultPending;
    if (!state.playing || playbackID !== state.playbackID) return;
    const context = state.primedAudio;
    state.primedAudio = null;
    let audio;
    if (context) {
      const response = await fetch(result.audio_url);
      if (!response.ok) throw new Error("The generated audio could not be loaded");
      const buffer = await context.decodeAudioData(await response.arrayBuffer());
      if (!state.playing || playbackID !== state.playbackID) return;
      audio = bufferedAudio(context, buffer);
    } else {
      audio = new Audio(result.audio_url);
    }
    state.audio = audio;
    updateAudioLoading();
    audio.addEventListener("play", () => {
      if (state.audio !== audio) return;
      state.playing = true;
      state.paused = false;
      syncPlaybackUI();
    });
    audio.addEventListener("pause", () => {
      if (state.audio !== audio || audio.ended || !state.playing) return;
      state.paused = true;
      elements.status.textContent = "Paused";
      syncPlaybackUI();
    });
    audio.onended = () => advance(playbackID);
    audio.onerror = () => failSpeech(playbackID, "The generated audio could not be played");
    if (continuePlaying && state.playing && state.playRequested) {
      state.paused = false;
      syncPlaybackUI();
      await audio.play();
      state.localRetryIndex = -1;
      state.localRetryCount = 0;
      elements.status.textContent = "";
      warmLocalAudioAhead(index);
    }
  }

  function releaseCurrentAudio() {
    if (!state.audio) return;
    const audio = state.audio;
    state.audio = null;
    audio.pause();
    audio.removeAttribute("src");
    updateAudioLoading();
  }

  function advance(playbackID) {
    if (!state.playing || playbackID !== state.playbackID) return;
    releaseCurrentAudio();
    state.index += 1;
    if (state.index >= state.sentences.length) {
      stopPlayback();
      elements.status.textContent = "Finished";
      return;
    }
    speakCurrent();
  }

  function failSpeech(playbackID, message) {
    if (playbackID !== state.playbackID) return;
    if (state.engine === "local") {
      const index = state.index;
      if (state.localRetryIndex !== index) {
        state.localRetryIndex = index;
        state.localRetryCount = 0;
      }
      if (state.localRetryCount < 1) {
        state.localRetryCount += 1;
        releaseCurrentAudio();
        evictLocalAudio(index);
        speakCurrent();
        return;
      }
      stopPlayback();
      elements.status.textContent = `Kokoro paused. Press Play to retry. ${message}`;
      return;
    }
    stopPlayback();
    elements.status.textContent = `Speech error: ${message}`;
  }

  function speakCurrent(waitForStartupBuffer = false) {
    if (!state.sentences[state.index]) {
      stopPlayback();
      return;
    }
    speechSynthesis.cancel();
    const playbackID = ++state.playbackID;
    highlight(state.index);
    state.playing = true;
    state.paused = false;
    state.playRequested = true;
    syncPlaybackUI();
    if (state.engine === "local") {
      elements.status.textContent = "";
      updateAudioLoading();
      speakLocal(playbackID, waitForStartupBuffer).catch((error) => failSpeech(playbackID, error.message));
      return;
    }
    const utterance = new SpeechSynthesisUtterance(state.sentences[state.index].text);
    utterance.rate = Number(elements.rate.value);
    const voice = selectedVoice();
    if (voice) utterance.voice = voice;
    utterance.onend = () => {
      if (!state.playing || playbackID !== state.playbackID) return;
      advance(playbackID);
    };
    utterance.onerror = (event) => {
      if (playbackID !== state.playbackID) return;
      if (event.error === "interrupted" || event.error === "canceled") return;
      stopPlayback();
      elements.status.textContent = `Speech error: ${event.error}`;
    };
    state.utterance = utterance;
    speechSynthesis.speak(utterance);
  }

  function togglePlayback() {
    if (state.playing && !state.paused) {
      state.playRequested = false;
      if (state.engine === "local" && state.audio) state.audio.pause();
      else speechSynthesis.pause();
      state.paused = true;
      syncPlaybackUI();
      elements.status.textContent = "Paused";
      return;
    }
    if (state.playing && state.paused) {
      state.playRequested = true;
      if (state.engine === "local" && state.audio) {
        state.audio.play();
        warmLocalAudioAhead(state.index + 1);
      } else speechSynthesis.resume();
      state.paused = false;
      syncPlaybackUI();
      return;
    }
    speakCurrent(true);
  }

  function playFrom(index) {
    stopPlayback();
    primeLocalAudio();
    state.index = Math.max(0, Math.min(index, state.sentences.length - 1));
    state.playing = true;
    speakCurrent();
  }

  function stopPlayback() {
    state.playbackID += 1;
    speechSynthesis.cancel();
    releaseCurrentAudio();
    releasePrimedAudio();
    state.playing = false;
    state.paused = false;
    state.playRequested = false;
    state.utterance = null;
    syncPlaybackUI();
  }

  function move(amount) {
    stopPlayback();
    state.index = Math.max(0, Math.min(state.index + amount, state.sentences.length - 1));
    highlight(state.index);
  }

  function loadVoices() {
    state.voices = speechSynthesis.getVoices().filter((voice) => voice.lang.startsWith("en"));
    state.voices.sort((a, b) => Number(b.localService) - Number(a.localService) || a.name.localeCompare(b.name));
    const selected = state.voices.find((voice) => voice.voiceURI === state.selectedVoiceURI);
    if (selected) {
      state.selectedVoice = selected;
      if (state.engine === "system") configureVoiceMenu();
      return;
    }
    const preferred = state.voices.find((voice) => voice.localService && /Samantha|Ava|Alex/i.test(voice.name));
    state.selectedVoice = preferred || state.voices[0] || null;
    state.selectedVoiceURI = state.selectedVoice?.voiceURI || null;
    if (state.engine === "system") configureVoiceMenu();
  }

  async function useSystemVoice(message = "") {
    stopPlayback();
    clearLocalAudio();
    state.engine = "system";
    state.modelID = "";
    state.localVoice = "";
    loadVoices();
    await savePreferences();
    renderModelCatalog();
    if (message) {
      elements.fallbackNotice.hidden = false;
      elements.fallbackNotice.textContent = message;
    }
  }

  function formatSize(bytes) { return `${Math.round(bytes / 1_000_000)} MB`; }

  function renderModelCatalog() {
    elements.modelCatalog.replaceChildren();
    state.models.forEach((model) => {
      const card = element("section", "voice-card");
      const copy = element("div");
      copy.append(element("h3", "", model.name), element("p", "", model.description));
      copy.append(element("p", "model-meta", `${formatSize(model.size_bytes)} download · ${model.license} · Hugging Face`));
      if (model.installed && model.install_path) {
        const path = element("p", "model-path");
        path.append("Stored at ", element("code", "", model.install_path));
        copy.append(path);
      }
      const actions = element("div", "model-actions");
      if (!model.supported) {
        actions.append(element("span", "model-meta", "Available on Apple silicon Macs"));
      } else if (!model.installed) {
        const install = element("button", "secondary", "Download");
        install.type = "button";
        install.addEventListener("click", () => installModel(model, install));
        actions.append(install);
      } else {
        const use = element("button", state.engine === "local" && state.modelID === model.id ? "primary" : "secondary", state.engine === "local" && state.modelID === model.id ? "In use" : "Use voice pack");
        use.type = "button";
        use.addEventListener("click", async () => {
          stopPlayback(); clearLocalAudio(); state.localVoice = state.modelID === model.id ? state.localVoice : model.default_voice;
          state.engine = "local"; state.modelID = model.id;
          configureVoiceMenu(); await savePreferences(); renderModelCatalog(); warmLocalAudioAhead();
        });
        const remove = element("button", "text-button", "Remove");
        remove.type = "button";
        remove.addEventListener("click", () => removeModel(model));
        actions.append(use, remove);
      }
      card.append(copy, actions);
      elements.modelCatalog.append(card);
    });
  }

  async function installModel(model, button) {
    if (!confirm(`Download ${model.name}? It uses about ${formatSize(model.size_bytes)} and comes from the approved Hugging Face catalog.`)) return;
    button.disabled = true;
    button.textContent = "Downloading…";
    elements.settingsStatus.textContent = `Downloading and checking ${model.name}. Keep this window open.`;
    const progressTimer = setInterval(() => updateInstallProgress(model).catch(() => {}), 500);
    try {
      const response = await fetch(`api/models/${model.id}/install`, { method: "POST" });
      if (!response.ok) throw new Error((await response.json()).error || "Download failed");
      const result = await response.json();
      model.installed = true;
      model.install_path = result.install_path;
      elements.settingsStatus.textContent = `${model.name} is ready.`;
      renderModelCatalog();
    } catch (error) {
      elements.settingsStatus.textContent = `${error.message}. Computer speech is still available.`;
      button.disabled = false;
      button.textContent = "Try again";
    } finally {
      clearInterval(progressTimer);
    }
  }

  async function updateInstallProgress(model) {
    const response = await fetch(`api/models/${model.id}/progress`, { cache: "no-store" });
    if (!response.ok) return;
    const progress = await response.json();
    if (progress.phase === "preparing") {
      elements.settingsStatus.textContent = `Checking ${model.name} download…`;
      return;
    }
    if (progress.phase === "verifying") {
      elements.settingsStatus.textContent = `Verifying ${model.name}…`;
      return;
    }
    if (progress.phase !== "downloading") return;
    const percent = progress.total_bytes ? Math.min(100, Math.round(progress.bytes_done / progress.total_bytes * 100)) : 0;
    elements.settingsStatus.textContent = `Downloading ${model.name}: ${percent}% · ${progress.files_done} of ${progress.total_files} files`;
  }

  async function removeModel(model) {
    if (!confirm(`Remove ${model.name} and recover about ${formatSize(model.size_bytes)}?`)) return;
    const response = await fetch(`api/models/${model.id}`, { method: "DELETE" });
    if (!response.ok) { elements.settingsStatus.textContent = (await response.json()).error; return; }
    model.installed = false;
    if (state.modelID === model.id) await useSystemVoice("That voice pack was removed, so Planreader switched to a computer voice.");
    renderModelCatalog();
  }

  async function loadSpeechSettings() {
    const response = await fetch("api/speech", { cache: "no-store" });
    if (!response.ok) return;
    const speech = await response.json();
    state.models = speech.models;
    state.engine = speech.preferences.engine;
    state.modelID = speech.preferences.model_id || "";
    state.localVoice = speech.preferences.voice || "";
    state.selectedVoiceURI = speech.preferences.system_voice || null;
    elements.rate.value = speech.preferences.rate || 1;
    elements.rateValue.textContent = `${Number(elements.rate.value).toFixed(2).replace(/0$/, "")}×`;
    if (speech.fell_back) {
      elements.fallbackNotice.hidden = false;
      elements.fallbackNotice.textContent = "Your saved voice pack is no longer available, so Planreader is using a computer voice.";
    }
    configureVoiceMenu();
    renderModelCatalog();
  }

  function bindControls() {
    elements.play.addEventListener("click", () => {
      if (!state.playing) primeLocalAudio();
      togglePlayback();
    });
    elements.previous.addEventListener("click", () => move(-1));
    elements.next.addEventListener("click", () => move(1));
    if ("mediaSession" in navigator) {
      navigator.mediaSession.setActionHandler("play", () => {
        if (!state.playing || state.paused) togglePlayback();
      });
      navigator.mediaSession.setActionHandler("pause", () => {
        if (state.playing && !state.paused) togglePlayback();
      });
      navigator.mediaSession.setActionHandler("stop", stopPlayback);
    }
    elements.voice.addEventListener("change", () => {
      if (state.engine === "local") {
        state.localVoice = elements.voice.value;
        clearLocalAudio();
        warmLocalAudioAhead();
        savePreferences().catch((error) => { elements.status.textContent = error.message; });
        return;
      }
      state.selectedVoiceURI = elements.voice.value;
      state.selectedVoice = state.voices.find((voice) => voice.voiceURI === state.selectedVoiceURI) || null;
      savePreferences().catch((error) => { elements.status.textContent = error.message; });
    });
    elements.rate.addEventListener("input", () => {
      elements.rateValue.textContent = `${Number(elements.rate.value).toFixed(2).replace(/0$/, "")}×`;
    });
    elements.rate.addEventListener("change", () => {
      if (state.engine === "local") {
        clearLocalAudio();
        warmLocalAudioAhead();
      }
      savePreferences().catch((error) => { elements.status.textContent = error.message; });
    });
    elements.speechSettings.addEventListener("click", () => elements.settingsDialog.showModal());
    elements.useSystem.addEventListener("click", () => useSystemVoice());
    elements.previewVoice.addEventListener("click", async () => {
      stopPlayback();
      const preview = "This is how Planreader will sound when it reads your document.";
      if (state.engine === "system") {
        const utterance = new SpeechSynthesisUtterance(preview);
        const voice = selectedVoice();
        if (voice) utterance.voice = voice;
        utterance.rate = Number(elements.rate.value);
        speechSynthesis.speak(utterance);
        return;
      }
      elements.status.textContent = "Preparing voice preview…";
      elements.previewVoice.disabled = true;
      elements.previewVoice.textContent = "Preparing…";
      try {
        const response = await fetch("api/synthesize", {method: "POST", headers: {"Content-Type": "application/json"}, body: JSON.stringify({text: preview, model_id: state.modelID, voice: state.localVoice, rate: Number(elements.rate.value)})});
        if (!response.ok) throw new Error((await response.json()).error || "Preview failed");
        const result = await response.json();
        state.audio = new Audio(result.audio_url);
        await state.audio.play();
        elements.status.textContent = "Playing preview";
      } catch (error) {
        releaseCurrentAudio();
        elements.status.textContent = `Kokoro preview unavailable. ${error.message}`;
      } finally {
        elements.previewVoice.disabled = false;
        elements.previewVoice.textContent = "Preview";
      }
    });
    const setSourceVisible = (visible) => {
      elements.sourcePane.classList.toggle("visible", visible);
      elements.sourceToggle.textContent = visible ? "Hide source" : "Show source";
      elements.sourceToggle.setAttribute("aria-expanded", String(visible));
      if (!visible) elements.sourceToggle.focus();
    };
    elements.sourceToggle.setAttribute("aria-controls", "source-pane");
    elements.sourceToggle.setAttribute("aria-expanded", "false");
    elements.sourceToggle.addEventListener("click", () => setSourceVisible(!elements.sourcePane.classList.contains("visible")));
    elements.sourceClose.addEventListener("click", () => setSourceVisible(false));
    document.addEventListener("click", (event) => {
      if (!elements.sourcePane.classList.contains("visible")) return;
      if (elements.sourcePane.contains(event.target) || elements.sourceToggle.contains(event.target)) return;
      setSourceVisible(false);
    });
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape" && elements.sourcePane.classList.contains("visible")) setSourceVisible(false);
    });
    elements.settingsDialog.addEventListener("click", (event) => {
      const bounds = elements.settingsDialog.getBoundingClientRect();
      const outside = event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top || event.clientY > bounds.bottom;
      if (outside) elements.settingsDialog.close();
    });
    window.addEventListener("beforeunload", () => speechSynthesis.cancel());
  }

  async function start() {
    if (!("speechSynthesis" in window)) {
      elements.status.textContent = "This browser does not support speech synthesis.";
      return;
    }
    bindControls();
    if (speechSynthesis.addEventListener) {
      speechSynthesis.addEventListener("voiceschanged", loadVoices);
    } else {
      speechSynthesis.onvoiceschanged = loadVoices;
    }
    loadVoices();
    await loadSpeechSettings();

    try {
      const response = await fetch("data.json", { cache: "no-store" });
      if (!response.ok) throw new Error(`Reader data returned ${response.status}`);
      const readerDocument = await response.json();
      const narration = readerDocument.narration;
      document.title = `${narration.title} — Planreader`;
      elements.title.textContent = narration.title;
      elements.filename.textContent = readerDocument.file_name;
      state.sentences = [];
      renderOrientation(narration);
      renderNarration(narration);
      renderRecap(narration);
      renderSource(readerDocument.sources);
      window.PlanreaderConversation?.start(readerDocument);
      highlight(0);
      elements.status.textContent = "Ready";
      warmLocalAudioAhead(0);
      if (readerDocument.agent_managed) startAgentLifecycle();
    } catch (error) {
      elements.title.textContent = "The reader could not load";
      elements.status.textContent = error.message;
    }
  }

  function startAgentLifecycle() {
    const sessionID = globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random()}`;
    const request = (method, keepalive = false) => fetch("api/agent/heartbeat", {
      method,
      headers: { "X-Planreader-Session": sessionID },
      keepalive,
    });
    let heartbeatTimer;
    request("POST").then((response) => {
      if (!response.ok) return;
      heartbeatTimer = window.setInterval(() => request("POST").catch(() => {}), 30_000);
      window.addEventListener("pagehide", () => {
        window.clearInterval(heartbeatTimer);
        request("DELETE", true).catch(() => {});
      }, { once: true });
    }).catch(() => {});
  }

  start();
})();
