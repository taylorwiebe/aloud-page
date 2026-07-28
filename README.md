# Planreader

Planreader turns a local Markdown document into a simpler spoken companion.
It uses the Claude Code or Codex installation and authentication already configured on your computer, then serves a private local reader with sentence highlighting. Computer speech works immediately; Apple silicon Mac users can optionally install a more natural local voice pack.

## Install

On an Apple-silicon Mac, ask Claude or Codex:

> Install Planreader by following https://github.com/taylorwiebe/planreader/blob/main/INSTALL.md

Or run the bootstrap installer yourself:

```bash
curl -fsSL https://github.com/taylorwiebe/planreader/releases/latest/download/install.sh | sh
```

This installs the latest verified release for the current user and configures the Planreader skill for detected Claude Code and Codex installations. It does not require a repository clone, Go, or administrator access. See [INSTALL.md](INSTALL.md) for update, source-build, security, and troubleshooting details.

## Requirements

- An Apple-silicon Mac for the release installer
- Claude Code or Codex installed and signed in through your company-approved account
- A browser with the Web Speech API, such as Safari or Chrome
- Go 1.25 or newer only when building from source

## Build

```bash
go build -mod=vendor -o planreader .
```

Install that source build and its skills with `./planreader install`.

## Use

```bash
./planreader path/to/plan.md
```

Choose the narration depth:

```bash
./planreader --depth briefing path/to/plan.md
./planreader --depth working path/to/plan.md
./planreader --depth full path/to/plan.md
```

Use your signed-in Codex account instead of Claude Code:

```bash
./planreader --provider codex --depth briefing path/to/plan.md
```

Tell the narrator what it may assume:

```bash
./planreader --audience "I know Go and Rails. Explain identity systems and internal Compound Engineering terminology." path/to/plan.md
```

Planreader prints the private local URL and normally opens it in your default browser.
Press Control-C in the terminal when you are finished.

## Speech choices

Open **Speech settings** in the reader to choose:

- **Computer voices:** no download and the fallback on every computer.
- **Kokoro 82M:** about 190 MB; a compact local model with 28 expressive American and British English voices.

Voice packs are pinned, integrity-checked downloads from Hugging Face and use the Apache 2.0 license. They are stored in your Planreader application settings directory and can be removed from the same settings panel. Your selected speech source, voice, and speed are remembered for future documents. If an installed model is moved, deleted, corrupt, or incompatible, Planreader switches back to a computer voice and keeps reading available.

## Data boundaries

- The original file is read in place and is never modified.
- Document text is passed to the selected AI command through standard input, not command arguments.
- Claude Code runs without tools or session persistence. Codex runs ephemerally in a temporary read-only workspace with structured output.
- The reader server binds only to `127.0.0.1` and uses a random URL token.
- Computer voices use the browser's speech support. Optional neural voices synthesize locally through sherpa-onnx; narration text and generated audio are not sent to Hugging Face or another speech provider.
- Model installation contacts Hugging Face but sends no document content.
- Planreader remembers speech preferences and installed model files. It does not persist the document, narration, generated audio, or reading progress. Temporary audio is deleted when the reader closes.

The exact model route and retention guarantees still depend on your organization's Claude Code or Codex configuration and policies.

## Attached-agent support

Planreader is adding conversations that remain owned by the exact agent task
that opened the reader. The task polls Planreader directly; Planreader does not
resume or launch a replacement session.

| Provider | Status | Evidence required |
|---|---|---|
| Codex | Supported for the current task only | Current `CODEX_THREAD_ID`, original conversation and workspace, project instructions and tools, normalized streamed events, native authorization, cancellation, and same-identity reconnect |
| Claude Code | Unsupported | A verified current-session identity plus the same end-to-end gates as Codex |

An installed provider or a resumable session is not sufficient evidence. A
provider is enabled only while its current task passes every gate.

Questions, selected original or friendly passages, and the source context needed
to answer them are intentionally returned to that already-attached task. The
task can use its existing conversation, repository workspace, project
instructions, and tools. If the reader requests a source or repository change,
the task can propose it, but Planreader waits for an immutable semantic decision
and the provider still enforces its native filesystem, network, and tool
authorization.

The task receives an owner-only descriptor path; its separate task secret is
never placed in command arguments or conversation text. Reader approvals apply
to one immutable semantic proposal and cannot override a native provider
authorization denial. Unrecognized provider events are rendered as
non-actionable status, and reconnect succeeds only for the same attachment and
task credential. Planreader never starts, resumes, or relabels another session
as a fallback.

The conversation transcript and draft are retained only for the active reader
session in memory and browser session storage. They are not written to a
Planreader transcript database and are discarded with that browser session.
The browser receives rendered source and friendly content, revisions, and
bounded selection mappings. It never receives the task secret, hidden agent
instructions, provider environment, canonical source path, unrestricted source
text, or source digest.

Prepared `data.json` remains readable but does not prove the identity or current
digest of an authoritative Markdown file. Without an attached supported task,
questions are unavailable. When prepared data is opened by an attached task,
questions can use its presentation content, but source and repository changes
remain unavailable because Planreader cannot safely verify or refresh the
authoritative source.

Ordinary launches without `--agent-managed` keep the original private,
read-only narration experience and expose no conversation endpoint.

## Development

```bash
go test -mod=vendor ./...
go vet -mod=vendor ./...
go build -mod=vendor ./...
```

The executable follows Cobra's conventional layout: top-level `main.go` delegates to `cmd/root.go`. Product code is grouped by responsibility under `internal/`:

- `internal/narration` turns Markdown into a structured human briefing through Claude Code or Codex.
- `internal/install` manages the current-user executable, shell path, and agent skills.
- `internal/release` verifies and activates published updates.
- `internal/reader` serves the private local reading interface and embeds its web assets.
- `internal/speech` owns voice models, preferences, downloads, and local synthesis.
