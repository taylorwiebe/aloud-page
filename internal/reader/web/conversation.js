(() => {
  "use strict";
  const $ = (selector) => document.querySelector(selector);
  const ui = { root: $("#conversation"), restore: $("#conversation-restore"), panel: $("#conversation-panel"),
    close: $("#conversation-close"), expand: $("#conversation-expand"), unread: $("#conversation-unread"),
    transcript: $("#conversation-transcript"), form: $("#conversation-form"), input: $("#conversation-input"),
    send: $("#conversation-send"), status: $("#conversation-status"), role: $("#conversation-role"),
    chip: $("#selection-chip"), selectionLabel: $("#selection-label"), selectionText: $("#selection-text"),
    selectionRemove: $("#selection-remove"), alert: $("#conversation-alert") };
  let saved = {};
  try { saved = JSON.parse(sessionStorage.getItem("planreader-conversation") || "{}"); } catch (_) {}
  const state = { mode: saved.mode || "compact", draft: saved.draft || "", selection: saved.selection || null,
    events: saved.events || [], cursor: saved.cursor || 0, active: false, controller: true, document: null,
    followUpRequested: false };
  const controllerID = globalThis.PlanreaderBrowserID ||
    sessionStorage.getItem("planreader-controller") ||
    (crypto.randomUUID?.() || `${Date.now()}-${Math.random()}`);
  let selectionTimer;
  let selectionRequest;
  globalThis.PlanreaderBrowserID = controllerID;
  sessionStorage.setItem("planreader-controller", controllerID);

  function persist() {
    state.draft = ui.input.value;
    sessionStorage.setItem("planreader-conversation", JSON.stringify({
      mode: state.mode, draft: state.draft, selection: state.selection, events: state.events, cursor: state.cursor,
    }));
  }
  function node(tag, className, text) {
    const result = document.createElement(tag);
    result.className = className;
    result.textContent = text;
    return result;
  }
  function announce(message, urgent = false) {
    ui.status.textContent = message;
    if (urgent) ui.alert.textContent = message;
  }
  function setMode(mode) {
    state.mode = mode;
    ui.root.className = `conversation conversation-${mode}`;
    ui.panel.hidden = mode === "compact";
    ui.restore.hidden = mode !== "compact";
    ui.restore.setAttribute("aria-expanded", String(mode !== "compact"));
    ui.expand.setAttribute("aria-pressed", String(mode === "expanded"));
    ui.expand.textContent = mode === "expanded" ? "Reduce" : "Expand";
    if (mode !== "compact") ui.unread.hidden = true;
    persist();
  }
  function showSelection() {
    ui.chip.hidden = !state.selection;
    if (!state.selection) return;
    ui.selectionLabel.textContent = state.selection.representation === "original" ? "Original" : "Friendly";
    ui.selectionText.textContent = state.selection.text;
  }
  function setEnabled(enabled = true) {
    ui.input.disabled = !enabled || !state.controller || state.active;
    ui.send.disabled = !enabled || !state.controller || state.active;
  }
  function addProposalEffect(message, label, effect) {
    if (!effect) return;
    const heading = node("h3", "proposal-effect-heading", label);
    const detail = node("pre", "proposal-effect", effect);
    message.append(heading, detail);
  }
  function finishTurn(message, urgent = false) {
    state.active = false;
    setEnabled();
    announce(message, urgent);
    if (state.followUpRequested) {
      state.followUpRequested = false;
      queueMicrotask(() => ui.input.focus());
    }
  }
  function renderEvent(event) {
    if (event.type === "progress") { announce(event.text || "Agent is working…"); return; }
    const kind = event.type === "proposal" ? "proposal" : event.type === "user" ? "user" : "agent";
    const message = node("article", `conversation-message ${kind}`,
      event.text || event.diff || event.type);
    if (event.type === "proposal" && event.proposal) {
      message.tabIndex = -1;
      addProposalEffect(message, "Source document effect", event.proposal.source_diff);
      addProposalEffect(message, "Friendly document effect", event.proposal.friendly_effect);
      if (!event.decided) {
        const actions = node("div", "proposal-actions", "");
        [["Approve", true], ["Reject", false], ["Continue discussing", null]].forEach(([label, approved]) => {
          const button = node("button", approved === true ? "primary" : "secondary", label);
          button.type = "button";
          button.addEventListener("click", () => decide(event, approved === true, message, approved === null));
          actions.append(button);
        });
        message.append(actions);
        queueMicrotask(() => message.focus());
        announce("Approval required. Review the proposed effects.", true);
      }
    } else if (event.type === "disconnected") announce("The attached task is disconnected. Reconnect it to continue; this transcript is preserved.", true);
    else if (event.type === "failed") finishTurn("The agent reported an error. Your conversation is preserved.", true);
    else if (event.type === "authorization_denied") finishTurn("Provider authorization was denied. No changes were applied.", true);
    else if (event.type === "cancelled" || event.interrupted) finishTurn("The response was interrupted. You can ask the agent to continue.", true);
    else if (event.type === "completed") finishTurn("Response complete.");
    ui.transcript.append(message);
    ui.transcript.scrollTop = ui.transcript.scrollHeight;
    if (state.mode === "compact") ui.unread.hidden = false;
  }
  async function decide(event, approved, proposalNode, continueDiscussion = false) {
    if (proposalNode.dataset.deciding === "true") return;
    proposalNode.dataset.deciding = "true";
    const proposalButtons = [...proposalNode.querySelectorAll("button")];
    proposalButtons.forEach((button) => { button.disabled = true; });
    setEnabled(false);
    try {
      const response = await fetch("api/conversation/browser/decisions", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ id: crypto.randomUUID(), action_id: event.proposal.id,
          controller_id: controllerID, proposal_digest: event.proposal.digest,
          document_revision: event.proposal.document_revision, approved }),
      });
      if (!response.ok) throw new Error(response.status === 409 ? "This proposal is stale or already decided." : "The approval could not be sent.");
      event.decided = true;
      persist();
      state.followUpRequested = continueDiscussion;
      announce(approved ? "Approved. Provider authorization may still be required." :
        continueDiscussion ? "Proposal declined. The composer will open when the agent is ready for your follow-up." : "Proposal rejected.", true);
      if (!continueDiscussion) ui.input.focus();
    } catch (error) {
      delete proposalNode.dataset.deciding;
      proposalButtons.forEach((button) => { button.disabled = false; });
      announce(error.message, true);
      proposalNode.focus();
    }
    finally { setEnabled(); }
  }
  async function submit(event) {
    event.preventDefault();
    const text = ui.input.value.trim();
    if (!text || !state.controller || state.active) return;
    const response = await fetch("api/conversation/browser/turns", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: crypto.randomUUID(), controller_id: controllerID, text,
        document_revision: state.document.document_revision, selection: state.selection }),
    });
    if (!response.ok) {
      const detail = await response.text();
      const observer = response.status === 409 && detail.includes("only the elected controller");
      announce(observer ? "Another tab controls this conversation. This tab is observer-only." :
        response.status === 409 ? "Another response is still active. Wait for it to finish." : "The message could not be sent.", true);
      if (observer) { state.controller = false; ui.role.textContent = "Observer tab"; }
      setEnabled(); return;
    }
    const userEvent = { id: `user-${crypto.randomUUID()}`, type: "user", text };
    state.events.push(userEvent);
    renderEvent(userEvent);
    ui.input.value = ""; state.selection = null; showSelection(); state.active = true; setEnabled(); persist();
  }
  async function poll() {
    try {
      const response = await fetch(`api/conversation/browser/events?after=${state.cursor}`, { cache: "no-store" });
      if (!response.ok) throw new Error();
      let changed = false;
      for (const event of await response.json()) {
        const cursor = Math.max(state.cursor, event.sequence || 0);
        if (cursor !== state.cursor) { state.cursor = cursor; changed = true; }
        if (!state.events.some((known) => known.id === event.id)) {
          state.events.push(event); renderEvent(event); changed = true;
        }
      }
      if (changed) persist();
    } catch (_) { announce("Connection lost. Reconnecting while preserving this conversation.", true); }
    window.setTimeout(poll, 1000);
  }
  function offsetWithin(root, selectionNode, offset) {
    const range = document.createRange();
    range.selectNodeContents(root); range.setEnd(selectionNode, offset);
    return range.toString().length;
  }
  async function captureSelection() {
    const selection = window.getSelection();
    if (!selection || selection.isCollapsed || !selection.toString().trim()) return;
    const range = selection.getRangeAt(0);
    const parent = range.commonAncestorContainer.nodeType === Node.ELEMENT_NODE ?
      range.commonAncestorContainer : range.commonAncestorContainer.parentElement;
    const friendlyBlock = parent?.closest("[data-block-index]");
    const friendly = friendlyBlock || parent?.closest("[data-friendly-section]");
    const original = parent?.closest(".source-section");
    const root = friendly || original;
    if (!root || !root.contains(range.startContainer) || !root.contains(range.endContainer)) return;
    const request = { quote: selection.toString(), representation: friendly ? "friendly" : "original",
      section_id: friendly ? friendly.dataset.friendlySection : original.id,
      block_index: friendlyBlock ? Number(friendlyBlock.dataset.blockIndex) : -1,
      start_offset: offsetWithin(root, range.startContainer, range.startOffset),
      end_offset: offsetWithin(root, range.endContainer, range.endOffset),
      revision: friendly ? state.document.friendly_revision : state.document.document_revision };
    const controller = new AbortController();
    selectionRequest = controller;
    try {
      const response = await fetch("api/conversation/browser/selection", {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(request),
        signal: controller.signal,
      });
      if (!response.ok) throw new Error();
      state.selection = await response.json();
      showSelection(); persist();
    } catch (error) {
      if (error.name !== "AbortError") announce("That selection is stale or cannot be mapped. Select the passage again.", true);
    } finally {
      if (selectionRequest === controller) selectionRequest = null;
    }
  }
  function scheduleSelection() {
    window.clearTimeout(selectionTimer);
    selectionRequest?.abort();
    selectionTimer = window.setTimeout(captureSelection, 0);
  }
  function start(documentData) {
    if (!documentData.agent_managed) return;
    state.document = documentData; ui.root.hidden = false; ui.input.value = state.draft;
    state.events.forEach(renderEvent); showSelection(); setMode(state.mode); setEnabled();
    ui.role.textContent = "This tab controls the conversation";
    ui.restore.addEventListener("click", () => setMode("open"));
    ui.close.addEventListener("click", () => setMode("compact"));
    ui.expand.addEventListener("click", () => setMode(state.mode === "expanded" ? "open" : "expanded"));
    ui.selectionRemove.addEventListener("click", () => { state.selection = null; showSelection(); persist(); ui.input.focus(); });
    ui.form.addEventListener("submit", submit);
    ui.input.addEventListener("input", persist);
    document.addEventListener("selectionchange", scheduleSelection);
    poll();
  }
  window.PlanreaderConversation = { start };
})();
