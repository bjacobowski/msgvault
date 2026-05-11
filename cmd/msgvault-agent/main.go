// Package main builds the msgvault-agent binary: a read-only subset
// of the msgvault command surface intended for agents with shell
// access. Both this binary and msgvault-omgnos share the cmd package
// and all internal/ packages; they differ only in which Register*
// functions they call and in the root command's identity.
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
