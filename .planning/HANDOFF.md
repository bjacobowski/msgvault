# msgvault fork — handoff

**Branch**: `omgnos` (in sync with `origin/omgnos` on
`bjacobowski/msgvault`)
**Last session**: 2026-05-11
**Repo**: `~/github.com/bjacobowski/msgvault`

## TL;DR

The fork now ships two binaries (`msgvault-omgnos` for human use,
`msgvault-agent` for AI agents with shell access) split out of one
source tree on the `omgnos` branch. The delete/dedup features have
been removed entirely. Recent agent-friendly CLI work has landed:
`--quiet`, schema-versioned JSON, `show-thread`, and a TUI `:` goto-
by-id bar. Working tree clean, all tests pass.

## Why this fork exists

Upstream is `wesm/msgvault`. The fork's goals:

1. **Eliminate destructive operations** (delete-from-Gmail, dedup-as-
   merge). The user does not use them and wants them gone from the
   surface, not just gated. **Done** in phase 1.
2. **Two binaries** from one source tree: a full-surface human
   binary and a read-only agent binary, so agents have a strict
   subset of capabilities by construction. **Done** in phase 2.
3. **Agent-friendly CLI/TUI ergonomics** — `--quiet`, versioned JSON,
   bulk lookups, ID-jump in TUI. **In progress** (4 items shipped;
   more items in the original 35-item proposal still queued).
4. **Local labeling/tagging** — separate track, not started. The user
   is also building an HTTP "screen share" UX for agents in a
   different project; the labeling design will likely intersect with
   that.

## What's shipped (most recent first)

### Track: TUI goto-by-id (`c5b9224`)

`:` opens an inline goto bar at any TUI view level. Accepts a numeric
internal ID, a Gmail hex source ID, or `t:<id>` / `thread:<id>` to
jump to the message's thread instead of its detail. Lookup via
existing `engine.GetMessage` / `GetMessageBySourceID`. Empty/Esc/
not-found/error all handled with a flash and no view change. 10
new tests in `internal/tui/goto_test.go`.

Out of scope on user's instruction: the agent binary doesn't expose
the TUI for typical agent use — agents will consume the upcoming
HTTP screen-share UX from a separate project.

### Track: `--quiet`/`-q` flag (`a1833bb`)

Global persistent flag. Silences the stderr slog stream
(`msgvault startup` / `msgvault exit` / per-command INFO lines).
File logging continues unchanged so the daily audit trail is
preserved. Panic recovery and cobra error output still write to
`os.Stderr` regardless.

Implementation: new `logging.Options.StderrDisabled` field skips the
stderr text handler when set. 2 new unit tests in
`internal/logging/logging_test.go`.

### Track: Schema-versioned JSON (`d4e641d`)

`"schema_version": 1` added as an additive sibling field on every
object-shaped CLI JSON response (`show-message`, `query`,
`search-by-domains`, hybrid search, `export-attachment`,
`show-thread`).

Array-shaped responses (`search`, `list-*`, `identity list`) stay
bare — adding versioning there requires a wrapper object, which is a
breaking change deferred until a v2 is needed. **Consumers treat
unversioned arrays as v1 by convention.**

`SchemaVersion` constant lives in `cmd/msgvault/cmd/output.go`.
Bumping requires reviewing the call sites and consumer expectations.
3 regression tests including a `TestSchemaVersion_Constant` that
locks the value at 1.

### Track: `show-thread <id>` command (`76e1f2b`)

CLI counterpart to the TUI's `T` keypress. Given an anchor message
ID (numeric or Gmail hex), resolves the message and lists every
other message in the same conversation, sorted oldest-first.

- `--json`: stamps `schema_version=1` alongside `conversation_id`,
  `source_conversation_id`, `subject`, `message_count`, `truncated`,
  and a `messages[]` array of summary rows.
- Text mode: tabwriter-aligned table with the thread subject as
  header.
- `--limit N`: caps the message count (default 1000, matching the
  TUI's `defaultThreadMessageLimit`).
- Local-only (`MustBeLocal`); remote mode not wired because the HTTP
  API doesn't expose a thread endpoint yet.

Both binaries register the command; it's a pure read path so the
agent gets it too. 4 regression tests.

### Phase 2: binary split (`141434c`)

Source tree now has two `cmd/` mains:

- `cmd/msgvault/main.go` → `msgvault-omgnos` binary (47 commands)
- `cmd/msgvault-agent/main.go` → `msgvault-agent` binary (~20
  commands, read-only)

Cobra registration moved from per-file `init() { rootCmd.AddCommand
(...) }` to exported `Register*(root *cobra.Command)` functions
called explicitly from each main. The `cmd` package no longer
auto-registers anything.

`cmd.SetIdentity(use, short, long)` lets each main customize the
cobra root's `Use`/`Short`/`Long`.

`cmd.EnableAgentMode()` / `cmd.IsAgentMode()`: toggle the agent
binary's `query` command consults to apply read-only DuckDB
guardrails — SQL statement allowlist
(`SELECT`/`WITH`/`EXPLAIN`/`SHOW`/`DESCRIBE`/`SUMMARIZE`),
multi-statement rejection, `WITH`-with-DML keyword scan, plus
session-level `disabled_filesystems=HTTPFileSystem,S3FileSystem` and
`lock_configuration=true`.

`Makefile`: `make build` builds both; `build-omgnos` /
`build-agent` / `install-omgnos` / `install-agent` for single-binary
ops. `flake.nix`: `packages.msgvault-omgnos`, `packages.msgvault-
agent`, `packages.default` (aliased to omgnos for backward compat).

### Phase 1: eliminate delete + dedup (`675626b`)

Removed in full: `delete-staged`, `cancel-deletion`, `list-
deletions`, `show-deletion`, `deduplicate`, `delete-deduped` CLI
commands; MCP `stage_deletion` tool; TUI deletion staging modal +
keybindings; `internal/deletion/` and `internal/dedup/` packages;
`internal/store/dedup.go`. ~9,300 LOC removed.

**Vestigial schema**: `deleted_at` and `delete_batch_id` columns
remain on `messages` table; `ALTER TABLE` migrations in `store.go`
preserved so legacy DBs upgrade cleanly. Rows that had `deleted_at`
set (dedup losers) are now visible in queries. Acceptable per user.

**Preserved**: `deleted_from_source_at` column and the IMAP-sync
helper `Store.UpdateMessageOnDedup` (unrelated to the removed
feature — handles cross-mailbox RFC822 dedup detection during sync).

### Phase 0 (predates these tracks)

Fork update from `wesm/main` merged at `bc0f0d4` brought in Accounts/
Identities/Collections/Dedup (PR #304), PST import, Facebook
Messenger DYI import. The dedup half was promptly removed (phase 1);
the rest stayed.

systemd user units at `~/.config/systemd/user/msgvault-sync.{service,
timer}` run hourly incremental sync (`Persistent=true`,
`RandomizedDelaySec=2m`) plus a `build-cache` ExecStartPost. Replaces
the old long-running `serve`-as-daemon pattern. `serve` itself is
preserved for the HTTP-screen-share work.

## How to resume

```bash
# Workspace
cd ~/github.com/bjacobowski/msgvault

# Read prior plans (they document decisions, not just to-do lists)
ls .planning/

# Build (nix devShell required for sqlite headers)
env -u GOROOT MISE_DISABLE_TOOLS=go nix develop --command bash -c 'make build'

# Run full tests with the same tags CI uses
env -u GOROOT MISE_DISABLE_TOOLS=go nix develop --command bash -c \
  'go test -count=1 -tags "fts5 sqlite_vec" ./...'

# New work: use a worktree off omgnos
git worktree add -b <branch> .worktrees/<branch> omgnos
# (worktrees live at ../msgvault.worktrees/<branch>)
```

## Conventions established this session

1. **Plans go to `.planning/PLAN-<topic>.md` in the worktree** before
   execution. Committed to git.
2. **Worktrees off `omgnos`** for each track; merge back with
   `--no-ff` to preserve granularity. Branch + worktree cleaned up
   after merge.
3. **Step-wise commits** within each track: each commit leaves
   `make build` green and `go test ./...` passing. Goal-friendly for
   `git bisect` later.
4. **Commit messages**: lead with one-line summary, then a paragraph
   on the why, then the *what* with file-level bullets. Test
   coverage and smoke-test notes included.
5. **Build incantation** (worth memorizing): `env -u GOROOT
   MISE_DISABLE_TOOLS=go nix develop --command bash -c '<cmd>'`. The
   env unset bypasses mise's Go 1.26 in favor of nix's pinned 1.25.9.
6. **Default to `-q`** when running CLI commands in scripts/tests —
   keeps stderr clean.

## Open tracks (not started)

### Other agent-friendly CLI items from the original 35-item list

- `list-recipients` command (parallel to `list-senders` /
  `list-domains` / `list-labels`). Real gap; ~1 hour of work.
- Bulk `show-message <id1> <id2> ...` — saves N round trips for
  agents pulling multiple messages. Touches `show_message.go` cobra
  Args + RunE.
- TUI prev/next-after-goto: currently `detailMessageIndex = -1` after
  a goto, so left/right arrows in detail view are no-ops. Could add
  conversation-level prev/next as a fallback.
- TUI: persistent goto history (`:` recalls last entry like `/` does
  for search).
- `--output-format=ndjson` for `search` / `list-*` so consumers
  stream rows. Composes well with `--quiet`.

### Phase 4: labeling/tagging feature design

User has said they want "all the labelling options" but the design
is open:

- **Local-only labels** — write to a new `local_labels` /
  `local_message_labels` table; never touch Gmail. Simplest. Useful
  for cross-account / cross-source tagging.
- **Write-back to Gmail** — touches the Gmail Modify API. Reversible
  if we keep the label as local-only-by-default and only push on
  explicit opt-in.
- **Both** — local labels by default; designated labels push to
  Gmail. Two-way sync needs conflict resolution.

The HTTP screen-share project (separate codebase) probably colors
this — agents will need a label write surface. Worth a brief design
chat before code.

### Docs cleanup: `docs/accounts-identities-collections-dedup/`

Upstream docs that bundle Accounts + Identities + Collections + Dedup
into one design narrative. The dedup half is now drift from reality
(feature gone). Surgical pruning is doable but not urgent.

### Future upstream merges

`wesm/main` may merge new features. Each upstream merge into
`omgnos` will need a quick triage:

- Anything destructive → drop or gate.
- Anything that re-introduces dedup → drop.
- Anything additive (new imports, new query helpers) → keep.

The current fork is `omgnos` 19+ commits ahead of upstream as of this
handoff; future upstream merges will need conflict resolution mostly
around the eliminated delete/dedup surface.

## Open questions when you resume

1. **Labeling design**: local-only, write-back, or both? Wait until
   HTTP screen-share project surfaces concrete needs?
2. **Push policy for `omgnos`**: currently push after every track
   merge. Should there be a "release" cadence (e.g. tag every N
   tracks)?
3. **Remote API for `show-thread`**: when the HTTP API gains a
   thread endpoint, wire `show-thread` to use it via the existing
   `IsRemoteMode()` branch (mirror `show-message`).

## Useful starting points by intent

| If you want to… | Look at |
|---|---|
| Understand the binary split | `.planning/PLAN-agent-binary-phase-2.md` |
| Understand what was eliminated | `.planning/PLAN-agent-binary-phase-1.md` |
| Add a new CLI command | `cmd/msgvault/cmd/show_thread.go` (most recent template) |
| Add an agent-mode guardrail | `cmd/msgvault/cmd/query.go` (validateAgentSQL + DuckDB lockdown) |
| Stamp schema_version on a new JSON output | `cmd/msgvault/cmd/output.go` (`SchemaVersion`) + the existing call sites |
| Touch the TUI | `internal/tui/keys.go`, `model.go`, `view.go`, `navigation.go` |
| Touch the engine layer | `internal/query/engine.go` (interface), `sqlite.go` (SQLite impl) |
| Wire something into both binaries | `cmd/msgvault/main.go`, `cmd/msgvault-agent/main.go`, `cmd/msgvault/cmd/main_test.go` (all three lists kept in sync) |
