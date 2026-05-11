// Package main builds msgvault-omgnos: the full-surface human binary.
// Registers every command in the cmd package. The companion binary
// msgvault-agent (cmd/msgvault-agent) registers only the read-only
// subset.
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
		"msgvault-omgnos",
		"Offline email archive tool",
		`msgvault-omgnos is an offline email archive tool that exports and stores
email data locally with full-text search capabilities. This is the
full-surface fork binary; see msgvault-agent for the read-only
subset intended for agent use.`,
	)

	root := cmd.RootCmd()

	// Account / identity management
	cmd.RegisterAddAccount(root)
	cmd.RegisterAddIMAP(root)
	cmd.RegisterAddO365(root)
	cmd.RegisterRemoveAccount(root)
	cmd.RegisterUpdateAccount(root)
	cmd.RegisterExportToken(root)
	cmd.RegisterIdentity(root)
	cmd.RegisterCollection(root)

	// Sync + serve
	cmd.RegisterSync(root)
	cmd.RegisterSyncFull(root)
	cmd.RegisterServe(root)

	// Setup + maintenance
	cmd.RegisterSetup(root)
	cmd.RegisterUpdate(root)
	cmd.RegisterInitDB(root)
	cmd.RegisterBuildCache(root)
	cmd.RegisterCacheStats(root)
	cmd.RegisterRebuildFTS(root)
	cmd.RegisterRepairEncoding(root)
	cmd.RegisterEmbed(root)
	cmd.RegisterCreateSubset(root)

	// Imports
	cmd.RegisterImport(root)
	cmd.RegisterImportEmlx(root)
	cmd.RegisterImportGvoice(root)
	cmd.RegisterImportImessage(root)
	cmd.RegisterImportMbox(root)
	cmd.RegisterImportMessenger(root)
	cmd.RegisterImportPst(root)

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
