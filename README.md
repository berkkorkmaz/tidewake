# tidewake

**What did your coding agents leave behind, and is it safe to remove?**

`tidewake scan` lists the processes, git worktrees and disk that Claude Code and Codex sessions left
on your machine. It ties each one to the session that created it and shows the proof. It is
read-only: it prints the command to clean each item and never runs it.

Example output (trimmed):

```
$ tidewake scan
PROCESSES LEFT BEHIND  11 found · 294.9 MB RAM in leftovers and suspects
  leftover claude a34ed489        pid 46142   9d  271.5 MB  postgres -D /opt/homebrew/var/postgresql@17
           tree: 6 processes
           why:  carries CLAUDE_CODE_SESSION_ID=a34ed489; no running Claude Code session has this id;
                 no Claude Code process above it (parent: launchd)
           note: serves :5432; it may be a service you use, so stop it yourself if not
  leftover claude b7ec50d0        pid 77162   5d  426.0 kB  tail -n +1 -f /private/tmp/claude-501/…/tasks/b4.output
           why:  runs in session b7ec50d0's scratch directory; no running Claude Code session has this id;
                 no Claude Code process above it (parent: launchd)
           run:  kill -TERM 77162
  + 10 detached process(es) whose session is still running (--all to list)

WORKTREES  29 removable (4.3 GB) · 3 dangling · 13 kept
  removable claude ~/src/api/.claude/worktrees/fix-auth   1.4 GB  (1.2 GB rebuildable)  idle 6d
            run: git -C ~/src/api worktree remove ~/src/api/.claude/worktrees/fix-auth
  kept: uncommitted files 6, touched recently 4, unpushed commits 2, ignored files 1

DISK  21.4 GB reclaimable
  docker images                          23.5 GB  14.2 GB reclaimable
      run:  docker image prune -a --filter until=168h
  codex old release 0.158.0              331.4 MB  331.4 MB reclaimable

TOTAL  27.6 GB disk and 1.1 GB RAM can be reclaimed
Nothing was changed: scan is read-only. Review each command before running it.
```

## Why

Agent harnesses start MCP servers, dev servers, headless browsers, test runners and `tail -f` loops,
and create a worktree per task. When a session crashes, is killed, or runs headless, some of that
survives: [claude-code#1935](https://github.com/anthropics/claude-code/issues/1935),
[codex#12491](https://github.com/openai/codex/issues/12491) and
[codex#30408](https://github.com/openai/codex/issues/30408) report tens of gigabytes of orphaned
processes. Vendors fix their own leaks quickly, but no single vendor sees what *your* agents started
through the shell, what Docker kept, or what three different worktree managers left on disk.

## Install

```sh
brew install --cask berkkorkmaz/tap/tidewake
```

or, with Go 1.27+:

```sh
go install github.com/berkkorkmaz/tidewake/cmd/tidewake@latest
```

macOS today; Linux is planned. The binary is not notarized yet; the cask clears the quarantine flag
so Gatekeeper does not block it.

## Commands

| Command | What it does |
|---|---|
| `tidewake scan` | Leftover processes, worktrees and disk, with proof and a cleanup command per row |
| `tidewake scan --all` | Also list kept worktrees and processes whose session is still running |
| `tidewake scan --json` | Machine-readable output |
| `tidewake scan --root ~/src/app` | Also check a repo no session has used yet |
| `tidewake doctor` | Your Claude Code / Codex versions against known leak fixes, MCP servers duplicated per session, which state sources were readable |

Set `TIDEWAKE_DEBUG=1` to print how long each stage took.

## How it decides

**Processes.** Claude Code and Codex export a session id to every child process
(`CLAUDE_CODE_SESSION_ID`, `CODEX_THREAD_ID`). tidewake reads only those allow-listed variables
(never tokens) and checks the session against the harness's own state: `claude agents --json`,
`~/.claude/sessions/<pid>.json` (with the process start time, so a reused PID never counts as alive),
and Codex's thread database. Then:

| Verdict | Meaning |
|---|---|
| `leftover` | Its session has ended and no harness process is above it |
| `suspect` | No session id, but adopted by launchd, its process group leader is gone, and it runs from an agent path such as a Claude scratchpad |
| `detached` | Its session is still running; shown only with `--all` |
| `stuck` | Exiting or zombie; only its parent can clear it |

A process without the variable is still attributed when it runs from a session's scratch directory
(`/tmp/claude-<uid>/<project>/<session-id>/`).

Never flagged: anything under a live Claude Code or Codex process, launchd services, other users'
processes, tidewake itself and the shell it runs in.

A row gets a `kill -TERM` command only when tidewake is sure. There is no command when session state
could not be read, when anything in the tree listens on a port (it may be a database or dev server you
use), or for a desktop app. Children are folded into one row, but the list stops at any process that
belongs to a running session or to a different session, and the row says how many were left out.
`kill` commands use PIDs from the scan, so rescan before running them later.

**Worktrees.** Found from every repo a session has worked in. A worktree is `removable` only when
all of these hold:

- not locked, and no running process has its working directory inside it
- no uncommitted or untracked files
- no ignored files outside rebuildable folders (`git worktree remove` deletes ignored files too,
  so a `.env` or a data dump keeps it)
- HEAD is contained in a remote branch, and every commit made in the worktree is on some branch, tag
  or remote (removing a worktree deletes its reflog, the last pointer to commits left behind by a checkout)
- git has not touched it for 48 hours (`--idle` to change)

Registrations whose folder is gone are listed as `dangling` with the `git worktree prune` command.

**Disk.** Old Codex releases that are neither `current` nor in use, Docker images and build cache
(week-old or budget-based prune commands), and the size of transcript stores. Stopped containers and
volumes are listed for review only, since both can hold data.

## Keeping up with releases

Everything that changes between harness versions lives in [`internal/rules/pack.json`](internal/rules/pack.json):
process signatures, state paths, and a table of known leaks with the version that fixed them.
A daily workflow reads the Claude Code changelog and Codex releases and opens a pull request listing
new lines about leaks, orphans, worktrees and cleanup. A person reviews them before the pack changes.

## Compared with

- **worktrunk**, **Claude Code / Codex built-in cleanup**: manage worktrees they created. tidewake
  reads across them and checks ignored files before suggesting removal.
- **abtop**: a live monitor of agent sessions. tidewake is a one-shot audit with proof per row.
- **Mole**, **npkill**, **kondo**: general disk cleaners that know nothing about agents.
- **ccusage**: tokens and cost. Not covered here.

## Roadmap

1. `tidewake clean`: run selected rows, with a preview, a receipt and undo (worktrees snapshot
   their HEAD to `refs/tidewake/` first).
2. Docker attribution: which containers an agent session started.
3. Optional background sweep and hook installer.
4. Linux.

## Safety

tidewake runs `ps` and `lsof` (in the C locale), `launchctl list`, `git` (with `--no-optional-locks`,
so it never rewrites an index), `claude agents --json`, `sqlite3 -readonly` and `docker system df`.
It does not write anywhere and sends nothing over the network. Set `TIDEWAKE_DEBUG=1` to see timings.

## License

MIT
