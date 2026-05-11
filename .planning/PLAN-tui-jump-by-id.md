# TUI: Jump to message / thread by ID

**Branch**: `tui-jump-by-id` (worktree off `omgnos`)
**Worktree**: `~/github.com/bjacobowski/msgvault.worktrees/tui-jump-by-id`
**Merges back to**: `omgnos`

## Goal

Give the human user a way to navigate directly to a known message or
thread inside the TUI by typing its ID. Today, the only path to a
message is to drill down through aggregates or use `/` search; if you
already have the ID (from a CLI invocation, a log line, or another
tool), you have no fast way to land on it.

The agent binary is explicitly out of scope — the user is building an
HTTP "screen share" thing as a separate project for agent use.

## UX

- **New keybinding**: `:` (colon) at any of `levelAggregates`,
  `levelDrillDown`, `levelMessageList`, `levelMessageDetail`,
  `levelThreadView`. Opens a "Go to ID" inline input bar at the
  bottom of the screen (same primitive as the existing inline search:
  `bubbles/textinput`).
- **Input formats** (mirrors `show-message <id>` CLI):
  - Numeric internal ID → `engine.GetMessage`
  - Gmail hex source ID (typically 16 hex chars, but any non-numeric
    string falls through) → `engine.GetMessageBySourceID`
  - `t:<id>` or `thread:<id>` prefix → look up the message by ID,
    then jump to its thread (using the message's `ConversationID`)
- **On success**: push a breadcrumb of the current state, then
  navigate to `levelMessageDetail` (or `levelThreadView` if the
  thread prefix was used). Existing `Esc`/`Backspace` returns the
  user to where they came from.
- **On not-found**: show a flash error message; stay in the current
  view.
- **Help**: add `:`-line to the TUI help modal.

The thread-prefix syntax (`t:12345`) is a strict superset of the
existing TUI thread flow — the user can already press `T` on a
message in any list to view its thread, so this is mainly useful when
the user only knows the thread/conversation ID and not a member
message ID. Even then, `:t <id>` resolves to "message by ID, then
jump to thread" rather than "thread by conversation ID" because we
already have `GetMessage`/`GetMessageBySourceID` but no engine method
for thread-by-conv-id. (The conversation isn't a thing in the engine
API; threads are derived from `messages.conversation_id`.)

## Implementation surface

### New / modified files

| File | Change |
|---|---|
| `internal/tui/navigation.go` | New input modal state: a separate `gotoInput textinput.Model` field on Model (parallel to `searchInput`); new `gotoActive bool` flag. |
| `internal/tui/keys.go` | Add `:` keybinding in each view-level handler that opens the goto bar; add a new `handleGotoKeys` for the input-active state (`Enter` commits, `Esc` cancels); route through `handleModalKeys` or top-level Update. |
| `internal/tui/model.go` | New `commitGoto()` method: parse the input, dispatch to one of `loadMessageDetail` / look-up-by-source-id / thread variant. New tea.Cmd that returns a `gotoResultMsg{detail, err}` so the lookup is async and respects the existing loading-state pattern. |
| `internal/tui/view.go` | Render the goto input bar when `gotoActive` is true (mirror inline-search bar rendering); add `:` to the help modal text. |
| `internal/tui/setup_test.go` | New test helper: `WithGotoActive(s string)` for builder; key constants for `:`. |
| `internal/tui/keys_test.go` (or `nav_*_test.go`) | Tests covering: open with `:`, type, Enter routes to detail; numeric → GetMessage; hex → GetMessageBySourceID; `t:` prefix → thread; not-found shows flash. Use `querytest.MockEngine` with `GetMessageFunc` / `GetMessageBySourceIDFunc`. |

### No engine-interface changes required.

`Engine` already exposes `GetMessage(int64)`, `GetMessageBySourceID(string)`,
and `GetMessageSummariesByIDs` for the rest. Conversation lookup
piggybacks on the resolved message's `ConversationID`.

### No new keybindings clash check

- `:` is currently unused in every view-level handler I inspected
  (aggregate, message-list, detail, thread). I'll grep to confirm
  during execution before adding the binding.

## Execution order

Each step leaves the build green and the existing TUI behavior intact.

1. **State scaffolding**: add `gotoInput`, `gotoActive` to `Model`,
   wire `bubbles/textinput` initialization, no UI yet. Commit.
2. **Open/close + render**: add `:` keybinding in all view-level
   handlers to open the goto bar; render the bar in `view.go`; `Esc`
   cancels and clears state. No actual lookup yet. Commit.
3. **Commit handler (numeric + hex paths)**: on `Enter`, parse the
   input, dispatch to `loadMessageDetail` (numeric) or a new
   `loadMessageDetailBySourceID` helper; on success push breadcrumb
   and jump to `levelMessageDetail`; on not-found show flash. Commit.
4. **Thread prefix support**: extend the parser to detect `t:` /
   `thread:` and route to a thread-view jump (look up the message,
   then `loadThreadMessages(msg.ConversationID)`). Commit.
5. **Help text + tests**: add `:` line to the help modal; add
   keys_test/nav_test coverage. Commit.

## Verification

- `msgvault-omgnos` builds; `make test` green.
- In a manual TUI session against the real DB: pressing `:`, typing
  a known internal numeric ID, pressing Enter lands on that message
  detail.
- Same flow with a Gmail hex ID lands on the same message.
- `:t <numeric-id>` lands on the thread view containing that
  message.
- An unknown ID flashes "message not found" and leaves the view
  unchanged.
- `Esc` while typing closes the bar without navigating.

## Risks

- **Goroutine/loading-state races** — TUI uses request IDs
  (`loadRequestID`) to invalidate stale async results. Goto lookups
  must follow the same pattern or they'll race with concurrent
  navigation.
- **Modal coexistence** — if a modal is already open when `:` is
  pressed, ignore the keybinding (`m.modal != modalNone` early
  return).
- **Inline-search vs goto** — `/` opens inline search, `:` opens
  goto. Both use textinput; ensure only one is active at a time.
- **Detail view "navigation"** — when current view is already
  `levelMessageDetail`, jumping to another detail must still push a
  breadcrumb so `Esc` returns to the previous detail, not the parent
  list. Verify this works.

## Out of scope

- Agent binary (`msgvault-agent`) — explicitly excluded per user
  ("agent is not likely to use TUI").
- Thread-by-conversation-id lookup that bypasses the message
  resolution step. If wanted later, easy to add via a new
  `GetMessageSummariesByConversationID` engine method.
- RFC822 Message-ID lookup (`<abc@gmail.com>`) — the store has
  `GetMessageIDByRFC822ID` but it's not on the Engine interface;
  promoting it is a small additive change deferred until needed.
- Fuzzy or partial-prefix matching of IDs.
