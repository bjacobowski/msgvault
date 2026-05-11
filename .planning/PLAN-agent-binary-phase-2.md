# Phase 2 — Binary split (msgvault-agent)

**Branch**: `phase-2-binary-split` (worktree off `omgnos`)
**Worktree**: `~/github.com/bjacobowski/msgvault.worktrees/phase-2-binary-split`
**Merges back to**: `omgnos`

## Goal

Produce a second binary, `msgvault-agent`, that exposes only the
read-only subset of msgvault's command surface. Both binaries share
`internal/` packages and the `cmd` package; they differ only in which
commands they register and in their root-command identity (`Use`,
`Long`).

After this phase:
- `make build` produces `msgvault-omgnos` AND `msgvault-agent` from the
  same source tree on the `omgnos` branch.
- The Nix flake exposes both.
- `msgvault-agent --help` shows only read paths. Any attempt at a
  destructive command fails with "unknown command".
- The existing `msgvault-omgnos` surface is unchanged — same commands,
  same flags, same behavior.

## Refactor shape

Today every command file in `cmd/msgvault/cmd/*.go` looks like:

```go
var fooCmd = &cobra.Command{ … }

func init() {
    fooCmd.Flags().StringVar(…)  // local flag setup
    rootCmd.AddCommand(fooCmd)   // implicit registration
}
```

`init()` fires whenever the `cmd` package is imported. If the agent
binary also imports `cmd`, every command gets registered — defeating
the point. The fix is to split implicit registration from flag setup:

```go
var fooCmd = &cobra.Command{ … }

func init() {
    fooCmd.Flags().StringVar(…)  // flag setup stays in init
}

// New: explicit registration, called by the binary's main()
func RegisterFoo(root *cobra.Command) {
    root.AddCommand(fooCmd)
}
```

The package-global `rootCmd` stays. `Execute()` and `ExecuteContext()`
keep working the way they always have. What changes: each binary's
main() must now explicitly call the `Register*` functions for the
commands it wants exposed.

Identity (`Use`, `Long`) differs per binary. Add a small
`SetIdentity(use, long string)` helper in the `cmd` package that
mutates `rootCmd` post-construction. Each main() calls it before
Execute.

## Command roster

### Agent binary surface (read-only)

| Command       | File                  | Notes |
|---------------|-----------------------|-------|
| `search`      | search.go             |  |
| `show-message`| show_message.go       |  |
| `list-messages` *(if exists — TBD)* | — | check inventory |
| `list-senders`| list_senders.go       |  |
| `list-domains`| list_domains.go       |  |
| `list-labels` | list_labels.go        |  |
| `list-accounts`| list_accounts.go     | inspection only |
| `stats`       | stats.go              |  |
| `verify`      | verify.go             | local integrity check |
| `export-eml`  | export_eml.go         | writes a file to local fs |
| `export-attachment` | export_attachment.go | writes a file |
| `export-attachments` | export_attachments.go | writes a zip |
| `query`       | query.go              | read-only DuckDB-against-Parquet; agent-safe by intent |
| `tui`         | tui.go                | read-only after phase 1 |
| `mcp`         | mcp.go                | read-only catalog after phase 1 |
| `logs`        | logs.go               | view-only |
| `version`     | version.go            |  |
| `quickstart`  | quickstart.go         | help text |
| `completion`  | completion.go         | shell completion script |

### Human binary only (write paths)

`add-account`, `add-imap`, `add-o365`, `update-account`,
`export-token`, `collection`, `identity`, `sync`, `sync-full`,
`serve`, `setup`, `update`, `init-db`, `build-cache`, `rebuild-fts`,
`repair-encoding`, `build-embeddings`, all the imports
(`import-emlx`, `import-gvoice`, `import-imessage`, `import-mbox`,
`import-messenger`, `import-pst`, `import-whatsapp`), and
`create-subset`.

### Open questions for the user

- **`query` in agent**: it's a DuckDB `Query()` (not `Exec()`), but
  DuckDB allows `COPY … TO` and `CREATE TABLE` over attached Parquet.
  Worst case: clobbers cache files (rebuilt by `build-cache`) or
  writes to the agent's own filesystem. I'm inclined to include it.
  Push back if you want it human-only.
- **`export-token`** in agent: I excluded it. Even though an agent with
  shell access can already cat the tokens file, exposing a CLI command
  to do so seems gratuitous. Confirm.
- **`completion`**: included in agent (shells need it).

## File inventory

### New files

- `cmd/msgvault-agent/main.go` — agent binary main package.
- `cmd/msgvault/cmd/register.go` (new) — `SetIdentity(use, long)` helper.

### Modified files

- `cmd/msgvault/cmd/root.go` — `rootCmd` stays, but its `Use` and `Long`
  become defaults that `SetIdentity` can override.
- Every command file with `init() { rootCmd.AddCommand(xCmd) }` (44
  files; root.go's own init is flag-setup only and stays as-is):
  - Remove `rootCmd.AddCommand(xCmd)` from `init()`.
  - Append `func RegisterX(root *cobra.Command) { root.AddCommand(xCmd) }`.
  - Where a file has multiple `AddCommand` calls (e.g.
    `build_cache.go` has 2 — second is likely a sub-command), bundle
    them into a single `Register*` that does both.
- `cmd/msgvault/main.go` — after Cmd auto-registration is gone, calls
  the full set of `Register*` functions before `cmd.ExecuteContext`.
- `Makefile` — add `msgvault-agent` target.
- `flake.nix` — add `msgvault-agent` package output.

## Execution order

Each step must leave both binaries building and tests passing.

1. **Add `Register*` to every command file**; keep the existing
   `rootCmd.AddCommand` in `init()` for now. Now both registration
   paths exist; the package still self-registers via init(). Commit.
2. **Add `cmd/msgvault-agent/main.go`** that calls the agent-safe
   `Register*` subset on its own root. Verify it builds (it will pick
   up the package-global rootCmd's init-registered commands too — so
   for this commit the agent binary ALSO has the full surface; that's
   fine as an intermediate state). Add Makefile target.
   Verify `msgvault-agent --help` works. Commit.
3. **Add `cmd/msgvault/cmd/register.go`** with `SetIdentity` helper.
   Have each main call it.
4. **Flip the cutover**: in every command file, remove
   `rootCmd.AddCommand(xCmd)` from `init()`. The package no longer
   auto-registers. Update `cmd/msgvault/main.go` to call every
   `Register*` explicitly. At this point the agent binary's surface
   actually shrinks to the read-only subset.
   Run both binaries' `--help` to verify. Commit.
5. **Flake.nix**: add `msgvault-agent` package output. Verify
   `nix build .#msgvault-agent` succeeds (skip if nix flake builds
   aren't tested locally; can be verified ad-hoc by the user).
   Commit.

## Verification

- `make build` succeeds, produces both `msgvault-omgnos` and
  `msgvault-agent`.
- `./msgvault-omgnos --help` shows the full pre-phase-2 surface
  (~38 commands).
- `./msgvault-agent --help` shows only the agent-safe roster
  (~17 commands).
- `./msgvault-agent sync foo@example.com` exits with cobra's
  "unknown command" error.
- `./msgvault-agent search foo --json` works against the existing
  `~/.msgvault/msgvault.db`.
- `go test -tags "fts5 sqlite_vec" ./...` still passes.

## Risks

- **44 mechanical edits.** A scripted sed pass is possible but each
  file's init() has flag-setup intermixed; doing it by hand catches
  the edge cases. Plan budget: an hour or two.
- **Hidden cross-references** between command files (one cmd calling
  another's helper) — unlikely given the cobra pattern, but the build
  will catch any.
- **`mcpCmd`'s long description** currently lists tool names; phase 1
  already trimmed `stage_deletion`. No further change needed.
- **`tuiCmd` and the agent**: TUI requires a terminal. Agents running
  in headless contexts will fail to start TUI. That's expected; the
  agent should know better than to invoke it from non-interactive
  contexts.

## Out of scope (deferred)

- **Agent-friendly CLI enhancements** (jump-by-id in TUI, `--quiet`
  flag, bulk show-message, schema-versioned JSON, `show-thread`
  command, etc.). Tracked in the original 35-item proposal list;
  handled in a later phase or piecemeal.
- **MCP catalog further scrubbing**: phase 1 already removed the only
  write tool. If new write tools land upstream, phase 3 (or a
  follow-up) handles them.
- **Labeling/tagging feature**: separate track entirely.
- **Removing `remove-account` from the fork**: user hasn't decided;
  it stays in `msgvault-omgnos` for now.

## Merge-back

When all steps land and verification passes:

```
git checkout omgnos
git merge --no-ff phase-2-binary-split
```

Push `omgnos` only when user confirms.
