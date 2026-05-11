// Package main builds msgvault-agent: the read-only subset of the
// msgvault command surface, intended for AI agents with shell access.
// Shares the cmd package and all internal/ packages with
// msgvault-omgnos; differs only in which Register* functions are
// called and in the root command's identity.
//
// Excluded from this binary by construction:
//   - All account / identity / collection mutations (add-*, update-*,
//     remove-*, identity, collection, export-token)
//   - Sync against remote sources (sync, sync-full, serve)
//   - Imports (import-*, import-emlx, etc.)
//   - Local mutators (init-db, build-cache, rebuild-fts,
//     repair-encoding, build-embeddings, setup, update, create-subset)
//
// The `query` command is included with read-only guardrails applied
// to its DuckDB session (statement allowlist + disabled_filesystems +
// locked configuration). The `tui` and `mcp` commands are included
// because their write surfaces were already removed in phase 1.
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/wesm/msgvault/cmd/msgvault/cmd"
)

const (
	exitCodeError       = 1
	exitCodeInterrupted = 130 // 128 + SIGINT, mirrors shell convention
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cmd.SetIdentity(
		"msgvault-agent",
		"Read-only msgvault binary for AI agents",
		`msgvault-agent is the read-only subset of msgvault, intended for
AI agents with shell access. It exposes only inspection and export
commands; account management, sync, imports, and local-DB mutators
are excluded. See msgvault-omgnos for the full surface.`,
	)
	cmd.EnableAgentMode()

	root := cmd.RootCmd()

	// Read paths
	cmd.RegisterSearch(root)
	cmd.RegisterShowMessage(root)
	cmd.RegisterStats(root)
	cmd.RegisterListAccounts(root)
	cmd.RegisterListDomains(root)
	cmd.RegisterListLabels(root)
	cmd.RegisterListSenders(root)
	cmd.RegisterVerify(root)
	cmd.RegisterExportEML(root)
	cmd.RegisterExportAttachment(root)
	cmd.RegisterExportAttachments(root)
	cmd.RegisterQuery(root)
	cmd.RegisterCacheStats(root)
	cmd.RegisterTUI(root)
	cmd.RegisterMCP(root)
	cmd.RegisterLogs(root)

	// Meta
	cmd.RegisterVersion(root)
	cmd.RegisterQuickstart(root)
	cmd.RegisterCompletion(root)

	if err := cmd.ExecuteContext(ctx); err != nil {
		if isSignalCanceled(err, ctx) {
			return exitCodeInterrupted
		}
		return exitCodeError
	}
	return 0
}

func isSignalCanceled(err error, ctx context.Context) bool {
	return errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled
}
