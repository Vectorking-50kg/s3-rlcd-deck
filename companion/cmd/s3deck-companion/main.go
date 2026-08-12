package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/deviceidentity"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
	companionruntime "github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/runtime"
)

var (
	version = "0.1.0-dev"
	commit  = "unknown"
)

const (
	managementTokenEnvironment = "S3DECK_MANAGEMENT_TOKEN"
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
		"address for the management Web",
	)
	deviceHubAddress := flags.String(
		"device-hub-address",
		"127.0.0.1:7780",
		"independent address for Deck device traffic",
	)
	allowLANManagement := flags.Bool(
		"allow-lan-management",
		false,
		"explicitly expose the management Web beyond loopback",
	)
	managementOrigin := flags.String(
		"management-origin",
		"",
		"exact browser Origin allowed for LAN management",
	)
	dataDirectory := flags.String(
		"data-directory",
		"",
		"protected directory for Companion identity and revocable device trust (defaults to the platform user-config directory)",
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
	managementToken := os.Getenv(managementTokenEnvironment)
	if managementToken == "" {
		fmt.Fprintf(stderr, "cannot configure Companion: %s is required\n", managementTokenEnvironment)
		return 2
	}
	resolvedDataDirectory := *dataDirectory
	if resolvedDataDirectory == "" {
		var directoryErr error
		resolvedDataDirectory, directoryErr = defaultDataDirectory()
		if directoryErr != nil {
			fmt.Fprintf(stderr, "cannot locate the Companion data directory: %v\n", directoryErr)
			return 2
		}
	}

	store, err := pairing.OpenFileStore(filepath.Join(resolvedDataDirectory, "pairing.json"))
	if err != nil {
		fmt.Fprintf(stderr, "cannot open pairing trust: %v\n", err)
		return 2
	}
	defer store.Close()
	identity, err := deviceidentity.LoadOrCreate(filepath.Join(resolvedDataDirectory, "device-hub-identity.json"))
	if err != nil {
		fmt.Fprintf(stderr, "cannot load Device Hub identity: %v\n", err)
		return 2
	}
	pairingService, err := pairing.New(pairing.Config{
		Store:                  store,
		CertificateFingerprint: identity.Fingerprint(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "cannot configure pairing: %v\n", err)
		return 2
	}
	application, err := companionruntime.New(companionruntime.Config{
		Version: version,
		Management: companionruntime.ManagementConfig{
			Address:       *managementAddress,
			AllowLAN:      *allowLANManagement,
			AllowedOrigin: *managementOrigin,
			AdminToken:    managementToken,
		},
		DeviceHub: companionruntime.DeviceHubConfig{
			Address: *deviceHubAddress,
		},
		Pairing: pairingService,
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

func defaultDataDirectory() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return "", errors.Join(errors.New("user configuration directory is unavailable"), err)
	}
	return filepath.Join(base, "s3-rlcd-deck"), nil
}
