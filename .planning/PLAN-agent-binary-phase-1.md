# Phase 1 — Eliminate delete + dedup from omgnos

**Branch**: `phase-1-elim-delete-dedup` (worktree off `omgnos`)
**Worktree**: `~/github.com/bjacobowski/msgvault.worktrees/phase-1-elim-delete-dedup`
**Merges back to**: `omgnos`

## Goal

Remove the deletion and deduplication features from the fork entirely. Nobody on this fork uses them; keeping them around is dead weight for both the human binary (`msgvault-omgnos`) and the future agent binary (`msgvault-agent`).

After this phase:
- No `delete-staged`, `cancel-deletion`, `list-deletions`, `show-deletion`, `deduplicate`, `delete-deduped` commands.
- No `internal/deletion/` or `internal/dedup/` packages.
- No `stage_deletion` / `deduplicate` MCP tools.
- No TUI deletion-staging keybindings, modal, or actions.
- Sync's detection of Gmail-side deletions (`deleted_from_source_at`) is **preserved** — that's independent of our delete feature.

## Scope decisions

### Remove
- `cmd/msgvault/cmd/deduplicate.go` (+ test)
- `cmd/msgvault/cmd/delete_deduped.go` (+ test)
- `cmd/msgvault/cmd/deletions.go` (+ test) — contains `delete-staged`, `cancel-deletion`, `list-deletions`, `show-deletion`
- `internal/deletion/` — entire package (executor, manifest, tests)
- `internal/dedup/` — entire package
- `internal/store/dedup.go`, `internal/store/dedup_test.go`, `internal/store/dedup_delete_test.go`
- TUI deletion staging: modal type, keybindings (probably `x` / `d` / similar), action handlers in `actions.go`, view rendering in `view.go`
- MCP `stage_deletion` and `deduplicate` tools in `internal/mcp/server.go` + `handlers.go`
- `cmd/msgvault/cmd/quickstart.md` mentions of these commands (and any other docs)

### Keep
- `deleted_from_source_at` column and the logic that sets it during sync (detects Gmail-side deletions via History API). This is observability of source state, not an action *we* take.
- `LiveMessagesWhere` predicate generation in `internal/store/live_messages.go`, but simplified to only consider `deleted_from_source_at` (dedup branch goes away).
- The `deleted_at` column itself in `schema.sql` — leave as a vestigial column for backward compatibility with existing user DBs. Stop writing to it; reads from it can be dropped from query paths. (Avoids a destructive ALTER TABLE migration.)

### Out of scope (deferred to later phases)
- Phase 2: binary split (`cmd/msgvault-agent/`, cobra `init()` → `Register*` refactor, Makefile + flake updates).
- Phase 3: MCP catalog auto-scrubbing for agent binary, agent quickstart.
- Phase 4 (separate track): labeling/tagging feature design.

## File inventory

**Pure deletions (rm -rf or single-file removal):**

| Path | LOC | Notes |
|---|---|---|
| `cmd/msgvault/cmd/deduplicate.go` | 643 | + test |
| `cmd/msgvault/cmd/delete_deduped.go` | 194 | + test |
| `cmd/msgvault/cmd/deletions.go` | 838 | + test |
| `internal/deletion/executor.go` | 421 | + test (1111 LOC) |
| `internal/deletion/manifest.go` | 419 | + test (897 LOC) |
| `internal/dedup/dedup.go` | 1341 | + tests (744 + 97 LOC) |
| `internal/store/dedup.go` | ? | + tests (`dedup_test.go`, `dedup_delete_test.go`) |

Total: ~6,705 LOC base + tests.

**Surgical edits (entangled — keep file, remove specific pieces):**

| Path | What to remove |
|---|---|
| `internal/store/store.go` | dedup/deletion API surface; method signatures, transactions |
| `internal/store/api.go` | dedup-related interface methods |
| `internal/store/messages.go` | references to dedup tombstones in queries |
| `internal/store/sources.go` | dedup-related source logic |
| `internal/store/collection.go` | dedup-related collection logic |
| `internal/store/identifier_match.go` | dedup match algorithm hooks |
| `internal/store/migrate_legacy_identity.go` | dedup migration paths |
| `internal/store/live_messages.go` | simplify `LiveMessagesWhere`: drop the dedup-only branches |
| `internal/store/schema.sql` | drop dedup-only tables/indexes; leave `deleted_at` column as vestigial |
| `internal/store/sources_test.go`, `identifier_match_test.go`, `store_test.go`, `migrate_legacy_identity_test.go` | strip dedup test cases |
| `internal/tui/model.go` | remove deletion-staging modal type, navigation snapshot fields if any |
| `internal/tui/view.go` | remove deletion modal rendering |
| `internal/tui/keys.go`, `text_keys.go` | remove deletion staging keybindings |
| `internal/tui/actions.go` | remove deletion action handlers |
| `internal/tui/actions_test.go`, `selection_test.go`, `setup_test.go` | strip deletion test cases |
| `internal/mcp/server.go`, `handlers.go`, `server_test.go` | remove `stage_deletion` and `deduplicate` MCP tools |
| `cmd/msgvault/cmd/quickstart.md` | drop mentions of removed commands |
| `README.md` / docs | drop mentions of removed commands |

## Execution order

Goal: each commit builds and tests pass.

1. **Strip MCP tools** (`internal/mcp/`). Smallest blast radius — MCP catalog is decoupled. Commit.
2. **Strip TUI deletion staging** (`internal/tui/*`). Modal + keys + actions + tests. Commit.
3. **Delete command-layer files** (`cmd/msgvault/cmd/{deduplicate,delete_deduped,deletions}.go` + tests). At this point the only remaining callers of `internal/deletion` and `internal/dedup` should be store-layer test files. Commit.
4. **Delete `internal/deletion/` package** outright. Commit.
5. **Delete `internal/dedup/` package** outright. Commit.
6. **Surgical store-layer cleanup**: `internal/store/dedup*.go` files removed; `live_messages.go` simplified; dedup helpers stripped from `store.go`, `api.go`, `messages.go`, `sources.go`, `collection.go`, `identifier_match.go`, `migrate_legacy_identity.go`. Schema dedup tables dropped, `deleted_at` column kept as vestigial. Tests pruned. Commit.
7. **Docs/quickstart pass**: README, quickstart, any other doc references. Commit.

Each step in nix devShell with the working incantation:
```
env -u GOROOT MISE_DISABLE_TOOLS=go nix develop --command bash -c 'make build && make test'
```

## Schema migration approach

**Existing user DBs already contain dedup state.** Two columns matter:

- `deleted_at` (dedup loser tombstone) — leave column in schema, stop reading/writing it. Rows with `deleted_at IS NOT NULL` will silently become visible again, which is the desired behavior (un-hiding "dedup losers" since dedup no longer exists). Acceptable: the user has confirmed they don't use dedup.
- `deleted_from_source_at` — unchanged.

**Dedup-specific tables/indexes** (whatever they are — to be enumerated during step 6): dropped via a one-shot migration that runs on store open. Tables can simply not be referenced; eventually a `--vacuum` or similar can clean up, but for now leave them be (sqlite ignores them).

Schema migrations in msgvault: TBD whether there's a migration framework already (no `internal/store/migrations/` dir). If not, this is "schema.sql edits + acceptance that fresh installs and existing installs read slightly different physical schemas, both work."

## Verification

After all commits:

1. `make build` succeeds (both `msgvault-omgnos` and any test binaries).
2. `make test` passes.
3. `msgvault-omgnos --help` shows no `delete-staged`, `cancel-deletion`, `list-deletions`, `show-deletion`, `deduplicate`, `delete-deduped`.
4. `msgvault-omgnos serve` starts; HTTP API still serves messages.
5. Spot-check: open a real DB (`~/.msgvault/msgvault.db`) — `msgvault-omgnos search "subject:test"` returns results including rows that were previously dedup-hidden.
6. TUI: `msgvault-omgnos` opens; no deletion modal possible; standard navigation works.
7. Sync still detects Gmail-side deletions: trigger a sync, check `deleted_from_source_at` is still being set for known-deleted messages (or accept that this is a code path that's hard to test, and just verify the column-write code path in `internal/sync/` is untouched).
8. MCP server (`msgvault-omgnos mcp`) starts; tool catalog excludes `stage_deletion` and `deduplicate`.

## Risks

- **Test entanglement**: dedup tests in `internal/store/` likely set up fixtures that other tests rely on. Pruning may require rewriting fixtures.
- **Hidden callers**: `internal/api/server.go` had no deletion routes in initial grep, but worth a second pass. Same for `internal/sync/`.
- **Schema column drift**: vestigial `deleted_at` is the path of least resistance, but if any non-trivial query still filters on it after step 6, behavior changes. Have to be vigilant in step 6.
- **TUI keybinding conflicts**: removing a keybinding may free up a key for accidental reassignment. Just verify keys don't get reused for other actions.

## Merge-back

When all steps land and verification passes:

```
git checkout omgnos
git merge --no-ff phase-1-elim-delete-dedup
# or: squash-merge if the 7-commit series feels too granular for omgnos history
```

Push `omgnos` only when user confirms.
