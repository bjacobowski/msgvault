package cmd

import (
	"os"
	"testing"

	"github.com/spf13/cobra"
)

// TestMain registers every command with rootCmd before the test
// suite runs. Production binaries (cmd/msgvault, cmd/msgvault-agent)
// call the relevant Register* functions from their main(); this
// TestMain plays the same role for the test binary so command-lookup
// tests (cmd.Find, Use/Flag assertions) see a fully-wired rootCmd.
func TestMain(m *testing.M) {
	registerAllForTesting(rootCmd)
	os.Exit(m.Run())
}

// registerAllForTesting is the test-binary equivalent of
// cmd/msgvault/main.go's registration list. Keep in sync with that
// file when commands are added or removed.
func registerAllForTesting(root *cobra.Command) {
	RegisterAddAccount(root)
	RegisterAddIMAP(root)
	RegisterAddO365(root)
	RegisterRemoveAccount(root)
	RegisterUpdateAccount(root)
	RegisterExportToken(root)
	RegisterIdentity(root)
	RegisterCollection(root)

	RegisterSync(root)
	RegisterSyncFull(root)
	RegisterServe(root)

	RegisterSetup(root)
	RegisterUpdate(root)
	RegisterInitDB(root)
	RegisterBuildCache(root)
	RegisterCacheStats(root)
	RegisterRebuildFTS(root)
	RegisterRepairEncoding(root)
	RegisterEmbed(root)
	RegisterCreateSubset(root)

	RegisterImport(root)
	RegisterImportEmlx(root)
	RegisterImportGvoice(root)
	RegisterImportImessage(root)
	RegisterImportMbox(root)
	RegisterImportMessenger(root)
	RegisterImportPst(root)

	RegisterSearch(root)
	RegisterShowMessage(root)
	RegisterStats(root)
	RegisterListAccounts(root)
	RegisterListDomains(root)
	RegisterListLabels(root)
	RegisterListSenders(root)
	RegisterVerify(root)
	RegisterExportEML(root)
	RegisterExportAttachment(root)
	RegisterExportAttachments(root)
	RegisterQuery(root)
	RegisterTUI(root)
	RegisterMCP(root)
	RegisterLogs(root)

	RegisterVersion(root)
	RegisterQuickstart(root)
	RegisterCompletion(root)
}
