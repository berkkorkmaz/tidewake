# tidewake

**What did your coding agents leave behind, and is it safe to remove?**

`tidewake scan` lists the processes, git worktrees and disk that Claude Code and Codex sessions left
on your machine. It ties each one to the session that created it and shows the proof. It is
read-only: it prints the command to clean each item and never runs it.

Example output (trimmed):

```
$ tidewake scan
PROCESSES LEFT BEHIND  15 found · 1.1 GB RAM in leftovers and suspects
  leftover claude a34ed489        pid 46142   9d  271.6 MB  postgres -D /opt/homebrew/var/postgresql@17
           tree: 6 processes
           why:  carries CLAUDE_CODE_SESSION_ID=a34ed489; no running Claude Code session has this id;
                 no Claude Code process above it (parent: launchd)
           note: listens on :5432; check nothing still uses it
           run:  kill -TERM 46142 5572 4969 4970 5573 5574
  suspect  claude -               pid 32272   3d  660.1 MB  Brave Browser --headless=new …
           why:  parent exited, adopted by launchd; its process group leader 32269 has exited;
                 runs from a Claude Code path

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
go install github.com/berkkorkmaz/tidewake/cmd/tidewake@latest
```

macOS today; Linux is planned.

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

Never flagged: anything under a live Claude Code or Codex process, launchd services, other users'
processes. A process listening on a port, or a desktop app, carries a caution. Children are folded
into one row with their total memory.

**Worktrees.** Found from every repo a session has worked in. A worktree is `removable` only when
all of these hold:

- not locked, and no running process has its working directory inside it
- no uncommitted or untracked files
- no ignored files outside rebuildable folders (`git worktree remove` deletes ignored files too,
  so a `.env` or a data dump keeps it)
- HEAD is contained in a remote branch
- git has not touched it for 48 hours (`--idle` to change)

Registrations whose folder is gone are listed as `dangling` with the `git worktree prune` command.

**Disk.** Old Codex releases that are neither `current` nor in use, Docker images, stopped
containers and build cache (with week-old or budget-based prune commands, never volumes), and the
size of transcript stores.

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

tidewake runs `ps`, `lsof`, `launchctl list`, `git` (with `--no-optional-locks`, so it never rewrites
an index), `claude agents --json`, `sqlite3 -readonly` and `docker system df`. It does not write
anywhere and sends nothing over the network.

## License

MIT
