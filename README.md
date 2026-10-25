# engram

Identities with memory for coding agents. An identity is an individual: you
issue it once, and it then keeps its own memories, notes and unfinished work
across sessions, so it builds expertise instead of starting cold.

Engram is a local CLI. It never calls a model.

## Quick start

```bash
task build
./engram identity new ada "Specialist in SQLite concurrency for Go services."

export ENGRAM_IDENTITY=ada
./engram boot                       # who you are, what you know, where you left off
./engram note "sqlite-vec needs CGO"
./engram remember "Deferred transactions that upgrade fail with SQLITE_BUSY at once." \
    --kind pitfall --scope global --tags sqlite,locking
./engram recall "why did my transaction fail"
./engram thread open "Decide how vectors are stored"
./engram handoff "Built recall. Open: vector storage."
```

`engram help` shows a whole session and every command. `engram help <command>`
gives one command's flags, examples and output, and `engram help concepts`
explains the terms. The help is written for an agent that has never seen
engram, and is generated from the same table that parses the flags.

## How an agent uses it

| Command | When |
|---|---|
| `boot` | At the start, and again after losing context. Prints the issued identity, self-description, other live sessions, open threads, the last handoff and a map of what the identity knows, cut to `--budget` tokens. |
| `recall` | Before assuming. Searches the current repository and global scope; `--all-scopes` for the rest. |
| `note` | Often. One line into the session journal. If the session ends without a handoff, its last notes stand in for one. |
| `remember` | For one durable claim, with a kind, a scope and where it came from. `--batch` saves many from JSON lines. |
| `memories` / `show` | To browse what is known without a query, and read one memory or thread in full. |
| `thread open` / `close` | For work that outlives the session. |
| `self show` / `self write` | To read and replace the identity's own description of itself. |
| `handoff` | Before finishing. |

## Design rules

- **An identity is an individual**, picked by you when you launch an agent.
  It may be live in several sessions at once; `boot` tells each about the
  others.
- **Files are the source of truth.** Every record is a file written once
  under a name no other writer picks, so concurrent sessions never wait on a
  lock. You can read, grep, edit, add and delete them by hand.
- **The index is disposable.** SQLite holds only a full-text index, built
  from the files when `recall` runs. Writes never touch it. Delete `.index/`
  whenever you like.
- **Nothing gates recall.** What a session remembers is recallable in the
  next one. Pruning happens afterwards.
- **Only the seed is instruction.** `identity.md` comes from you. Everything
  else was written by the identity and is presented to it as its own notes.
- **Scope keeps repositories apart.** A memory belongs to the repository it
  was learned in (named by its origin remote, so clones and worktrees agree)
  unless it is remembered with `--scope global`. A repository is also known
  by the path of its main worktree, so what was remembered before it had a
  remote stays in reach.

## On disk

```
$XDG_DATA_HOME/engram/            (or $ENGRAM_HOME)
  identities/<name>/
    identity.md                   seed, issued by you
    self.md                       the identity's own description
    self-history/                 every version of it that was written
    memories/<date>-<id>.md       one claim each, YAML frontmatter + body
    threads/<date>-<id>.md        unfinished work
    threads/<date>-<id>.closed.md how it ended
    sessions/<start>-<id>/
      meta.json                   cwd, scope, harness, Legato task/execution
      journal.jsonl               append-only: notes and what the session did
      handoff.md
    sessions/.keys/               which session each agent run is bound to
    .index/                       derived; safe to delete
```

A memory file you write yourself needs no frontmatter: it is loaded as a
global fact the identity was told.

## Sessions

An agent's shell keeps no variables between calls, so engram finds the session
from the environment: `ENGRAM_SESSION`, then `LEGATO_EXECUTION_ID`, then
`CLAUDE_CODE_SESSION_ID`. Each names one agent run, and `boot` resumes it.
Without any of them the tmux pane or working directory is used, and a session
is resumed only if it was active in the last 12 hours and has no handoff.

A session started under Legato records `LEGATO_TASK_ID` and
`LEGATO_EXECUTION_ID`, so Legato can set `ENGRAM_IDENTITY` at launch and add
the output of `engram boot` to the agent's prompt.

## Development

```bash
task test        # go test ./...
task test:race
task check       # build, test, vet, lint
```

Tests drive the CLI the way an agent does, against a temporary store.

## Not built yet

Semantic search, `confirm`/`refute`/`supersede`, consolidation of a session's
notes into memories, artifacts, and `who` (which identity suits a task).
