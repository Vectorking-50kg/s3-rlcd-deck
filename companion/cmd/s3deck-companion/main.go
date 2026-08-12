package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	companionruntime "github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/runtime"
)

var (
	version = "0.1.0-dev"
	commit  = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(arguments []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("s3deck-companion", flag.ContinueOnError)
	flags.SetOutput(stderr)
	showVersion := flags.Bool("version", false, "print build identity and exit")
	managementAddress := flags.String(
		"management-address",
		"127.0.0.1:7777",
		"loopback address for the management Web",
	)
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "s3deck-companion does not accept positional arguments")
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "s3deck-companion %s (commit %s)\n", version, commit)
		return 0
	}

	application, err := companionruntime.New(companionruntime.Config{
		ManagementAddress: *managementAddress,
		Version:           version,
	})
	if err != nil {
		fmt.Fprintf(stderr, "cannot configure Companion: %v\n", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err = application.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "Companion stopped with an error: %v\n", err)
		return 1
	}
	return 0
}
