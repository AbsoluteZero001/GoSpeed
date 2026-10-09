// Command gospeed is the GoSpeed command line client and local test server.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/AbsoluteZero001/GoSpeed/internal/cli"
)

func main() {
	// Ctrl+C (and SIGTERM on Unix) cancels the running test or stops the
	// server through the same context.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.NewApp(os.Stdout, os.Stderr).Run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}
