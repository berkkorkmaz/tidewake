# tidewake

[![ci](https://github.com/berkkorkmaz/tidewake/actions/workflows/ci.yml/badge.svg)](https://github.com/berkkorkmaz/tidewake/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/berkkorkmaz/tidewake?include_prereleases&label=release)](https://github.com/berkkorkmaz/tidewake/releases)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

**Your coding agents leave things running. tidewake finds them, with proof.**

Claude Code and Codex start servers, browsers, test runners and git worktrees. When a session ends
or crashes, some of it keeps eating RAM and disk. tidewake shows you what, which session left it,
and the exact command to clean it up. It never deletes anything itself.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/img/summary-dark.svg">
  <img alt="129 GB RAM in one Claude process, 1,300+ zombie Codex processes, 154 orphaned Claude Code processes, 30.5 GB found in one tidewake scan" src="docs/img/summary-light.svg">
</picture>

<sub>From user reports: [claude-code#11315](https://github.com/anthropics/claude-code/issues/11315),
[codex#12491](https://github.com/openai/codex/issues/12491),
[claude-code#17391](https://github.com/anthropics/claude-code/issues/17391#issuecomment-3821449896).
The last tile is one tidewake scan on the author's Mac.</sub>

## Install

```sh
brew install --cask berkkorkmaz/tap/tidewake
```

```sh
tidewake scan        # what agents left behind, and how to clean it
tidewake sessions    # live sessions: memory, CPU, which one is stuck
tidewake doctor      # known leaks in your Claude Code / Codex version
```

## One scan, 30.5 GB

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/img/freed-dark.svg">
  <img alt="Disk that can be freed: unused Docker images 14.2 GB, scratch files of ended Claude sessions 9.3 GB, old Codex releases 3.3 GB, Docker build cache 2.3 GB, finished git worktrees 1.5 GB" src="docs/img/freed-light.svg">
</picture>

## Proof on every row

```
leftover claude b7ec50d0   pid 77162   5d   tail -n +1 -f /private/tmp/claude-501/…/b4.output
         why:  runs in session b7ec50d0's scratch directory; no running Claude Code session has
               this id; no Claude Code process above it (parent: launchd)
         run:  kill -TERM 77162
```

## Safe by design

- **Never deletes.** It prints commands; you decide.
- **Won't guess.** Anything serving a port, like a database, gets a note instead of a command.
- **Protects your work.** A worktree is kept if it has uncommitted changes, unpushed or
  reflog-only commits, or ignored files like `.env`. On one Mac it kept 24 that way.
- **Stays local.** No network, no telemetry, and no iCloud downloads triggered.

Every rule is in [how it works](docs/how-it-works.md).

## Roadmap

`tidewake clean` with preview and undo · a context audit of what loads before you type · Linux

MIT licensed. macOS (Apple Silicon and Intel).
