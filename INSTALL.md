# Install Planreader

Planreader currently supports Apple-silicon Macs. It installs for the current user, does not require administrator access, and configures the Planreader skill for detected Claude Code and Codex installations.

## Ask Claude or Codex to install it

Give your agent this instruction:

> Install Planreader by following https://github.com/taylorwiebe/planreader/blob/main/INSTALL.md. Use the supported installer, keep all permission prompts visible, and report which Claude and Codex skills were configured.

The agent should run the public bootstrap installer and relay its result. It must not bypass shell, filesystem, or macOS security approvals.

## Install from a terminal

```sh
curl -fsSL https://github.com/taylorwiebe/planreader/releases/latest/download/install.sh | sh
```

The installer downloads the latest signed and notarized Apple-silicon release, verifies its checksum and Apple Developer identity, and installs it under your user account. It adds `~/.local/bin` to `~/.zprofile` when needed. Open a new terminal afterward; the current terminal can use the absolute command path printed by the installer.

Start a new Claude or Codex session after installation so it can load the new skill.

The installed skill includes the same-task conversation loop shipped with the
Planreader executable. Codex can attach only when its current task passes the
capability gate. Claude Code can still create ordinary private narrations, but
attached conversation is currently unsupported and is never replaced with a
resumed or newly launched session.

Planreader installs its skill into these detected user locations:

- Claude Code: `~/.claude/skills/read-with-planreader`
- Codex: `$CODEX_HOME/skills/read-with-planreader` when `CODEX_HOME` is set, otherwise `~/.codex/skills/read-with-planreader`

It will not overwrite a same-named skill that it does not own. Resolve the reported collision and run installation again.

## Update

Check without changing anything:

```sh
planreader update --check
```

Install the latest verified release and matching skills:

```sh
planreader update
```

You can also ask Claude or Codex to “update Planreader.” Start a new agent session after an update so it loads the refreshed skill. Re-running the bootstrap installer is safe and converges on the latest release.

Installation and update replace a Planreader-managed skill as one unit with the
embedded copy from that release, so the executable and its bridge loop stay in
sync. They do not overwrite a same-named skill Planreader does not own.

Source builds identify themselves as such. Switching one to the official release requires an explicit choice:

```sh
planreader update --replace-source
```

## Build from source

Use this fallback when you intentionally want a development build. Go 1.25 or newer is required.

```sh
git clone https://github.com/taylorwiebe/planreader.git
cd planreader
go build -mod=vendor -o planreader .
./planreader install
```

An agent may use a temporary checkout for these steps and remove it after installation. Building from source is not the normal installation path.

## Troubleshooting

- Run `planreader version` to see whether the active executable is an official release or a source build.
- Run `planreader install` again to repair Planreader-managed skills and the command link.
- If your shell is not zsh, add `~/.local/bin` to its `PATH` manually using the command reported by the installer.
- Installing or signing into Claude Code or Codex remains your responsibility; Planreader never changes provider authentication.
