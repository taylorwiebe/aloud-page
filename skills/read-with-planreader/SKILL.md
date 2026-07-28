---
name: read-with-planreader
description: Turn a local Markdown document into Planreader's clear, private spoken companion and manage the Planreader installation. Use when the user asks to open, read, listen to, simplify, narrate, brief, or make an audiobook from Markdown, or asks to install, update, check, or repair Planreader.
---

# Read with Planreader

Use Planreader as the single source of truth for preparing the human-readable narration. Do not summarize the document separately or reproduce Planreader's narration prompt in this skill.

## Manage Planreader

- When the user asks to update, check, or repair Planreader, run `planreader update` from their current working directory.
- Relay the version result and every Claude or Codex integration status. Never hide a `needs attention` or `not detected` result.
- After an install or update changes a skill, tell the user to start a new Claude or Codex session to load it.

## Run the document

1. Resolve the requested Markdown path. Treat relative paths as relative to the user's current working directory.
2. Confirm that the source is a readable regular `.md` file. Never modify it.
3. Select the provider that matches the current agent:
   - In Claude Code, pass `--provider claude`.
   - In Codex, pass `--provider codex`.
   - Never switch providers to bypass authentication, quota, or policy failures unless the user explicitly asks.
4. Run the installed Planreader command from the user's current working directory:

   ```sh
   planreader --agent-managed --provider PROVIDER --depth working DOCUMENT.md
   ```

   Use `briefing` for a quick overview, `working` by default, and `full` only when the user requests comprehensive detail. Add `--audience` when the user states what they know or which jargon needs explanation.
5. Keep the process handle returned by the command. Wait for `Reader ready:` and open or navigate to that URL when the environment permits it. If Planreader already opened the browser, do not open a duplicate tab. Agent-managed mode replaces an older agent-launched reader and stops automatically after its browser tab disappears.
6. Tell the user that the reader is ready and which provider was used. Do not dump generation logs or narration into chat.

## Attach this task

- After the reader reports that its conversation bridge is ready, run
  `planreader bridge probe --provider PROVIDER`.
- Treat that probe as the capability handshake. Continue only when it reports
  this current task as supported, and use only the capabilities it returns.
  Codex is currently supported when the probe identifies the current
  `CODEX_THREAD_ID`. Claude is currently unsupported: say so visibly and stop
  attachment. Never resume, fork, or start another provider session as a
  fallback.
- Keep the descriptor path printed by Planreader only for bridge commands. Pass
  it as `--descriptor PATH`; never print or read the descriptor contents, copy
  its task secret into chat or command arguments, or persist it. The descriptor
  is deleted when the reader exits.
- Keep this exact task inside the following loop, preserving its prior
  conversation, working directory, project instructions, tools, and native
  authorization:
  1. Run `planreader bridge wait --descriptor PATH` and decode the returned
     turn. A closed, disconnected, or cancelled reader ends the loop; never
     attach a different task.
  2. Publish a `progress` event before work and as meaningful work advances.
     Publish only the normalized event types `progress`, `text`, `proposal`,
     `completed`, `failed`, `cancelled`, and `authorization_denied`, with stable
     event and turn IDs and strictly increasing sequence numbers.
  3. Answer questions using this task's existing context. Stream safe answer
     chunks as `text` events. Treat unknown provider-native events as
     conservative progress text only; they must never create a proposal,
     approval, or action transition.
  4. Before an edit or other consequential action, publish one immutable
     `proposal`. Then run
     `planreader bridge decision --descriptor PATH --action ACTION_ID`. Execute
     only an approved decision whose proposal digest and document revision
     still match.
  5. A reader approval records semantic intent only. Request and obey every
     native Codex authorization normally. If native authorization is denied,
     publish `authorization_denied` and do not report completion or retry by
     changing providers.
  6. Publish exactly one terminal event: `completed`, `failed`, `cancelled`, or
     `authorization_denied`. On cancellation, interrupt in-flight work before
     publishing `cancelled`. Then wait for the next turn unless the reader has
     disconnected or closed.
- Send event JSON on standard input to
  `planreader bridge publish --descriptor PATH`. Provider-specific event
  translation stays here at the skill boundary; the reader receives only the
  normalized contract above.
- A Planreader approval records the reader's intent for one proposal. It never
  replaces or bypasses Claude or Codex permission prompts.
- Reconnect only through the same descriptor, attachment identity, task secret,
  and current task. If any identity changes, fail closed instead of relabeling
  the new task.

## Own the reader process

- Never launch Planreader in the background without retaining its process or session handle.
- Before the Claude or Codex session ends, interrupt any Planreader process that this session launched and wait for it to exit. Do this even after an error or an interrupted task.
- When the bridge loop ends, stop in-flight work, publish cancellation when the
  bridge still accepts it, interrupt the owned Planreader process, wait for it
  to exit, and confirm that its private descriptor was removed.
- When the user says they are done reading, close the reader process immediately.
- Do not stop a manually launched Planreader process. Agent cleanup applies only to commands started with `--agent-managed` by the current session.
- If cleanup cannot be confirmed, tell the user instead of silently leaving a process behind.

## Preserve narration quality

Let Planreader create the actual document the user will hear. Its application-owned prompt resolves references, explains jargon, preserves consequential facts, identifies assumptions and uncertainty, retains valuable diagrams, adds recall pauses, and separates decisions, actions, and verification items. If these requirements need to change, update and test the narration code rather than overriding it here.

## Reuse prepared narration

Use `--prepared DATA.json` only when the user asks to avoid regeneration and the prepared data is known to belong to the same source document. Never substitute unrelated or stale prepared narration.

## Handle failures

- If the selected provider is missing, signed out, rate-limited, or out of quota, report that exact condition and leave the provider unchanged.
- If browser opening fails but `Reader ready:` was printed, give the user the local URL.
- If narration generation fails, do not fall back to a chat summary as though the reader succeeded.
- Treat the browser's computer-voice fallback as a speech-setting issue, not a narration-generation failure.
