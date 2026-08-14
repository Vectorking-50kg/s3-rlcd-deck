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
	goruntime "runtime"
	"syscall"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/codexappserver"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/codexobserver"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/desktop"
	desktopassets "github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/desktop/assets"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/deviceidentity"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/managementtoken"
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
	// AppKit and the Windows message pump both require the desktop shell to
	// remain on its process main thread. Headless mode is harmlessly pinned too.
	goruntime.LockOSThread()
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(arguments []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("s3deck-companion", flag.ContinueOnError)
	flags.SetOutput(stderr)
	showVersion := flags.Bool("version", false, "print build identity and exit")
	headless := flags.Bool("headless", false, "run in the foreground without the menu-bar or tray shell")
	managementAddress := flags.String(
		"management-address",
		"127.0.0.1:7777",
		"address for the management Web",
	)
	deviceHubAddress := flags.String(
		"device-hub-address",
		"0.0.0.0:7780",
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
	resolvedDataDirectory := *dataDirectory
	if resolvedDataDirectory == "" {
		var directoryErr error
		resolvedDataDirectory, directoryErr = defaultDataDirectory()
		if directoryErr != nil {
			fmt.Fprintf(stderr, "cannot locate the Companion data directory: %v\n", directoryErr)
			return 2
		}
	}
	instance, err := desktop.AcquireSingleInstance(resolvedDataDirectory)
	if err != nil {
		fmt.Fprintf(stderr, "cannot start Companion: %v\n", err)
		return 2
	}
	defer instance.Close()

	managementTokenValue := os.Getenv(managementTokenEnvironment)
	if managementTokenValue == "" {
		managementTokenValue, err = managementtoken.LoadOrCreate(
			resolvedDataDirectory,
		)
		if err != nil {
			fmt.Fprintf(stderr, "cannot load the management token: %v\n", err)
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
	tlsCertificate, err := identity.TLSCertificate()
	if err != nil {
		fmt.Fprintf(stderr, "cannot load Device Hub TLS certificate: %v\n", err)
		return 2
	}
	pairingService, err := pairing.New(pairing.Config{
		Store:                  store,
		CertificateFingerprint: identity.Fingerprint(),
		CertificateDER:         identity.CertificateDER(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "cannot configure pairing: %v\n", err)
		return 2
	}
	codexCollector, err := codexappserver.New(codexappserver.Config{
		AdapterVersion: codexappserver.AdapterVersion,
		ClientVersion:  version,
	})
	if err != nil {
		fmt.Fprintf(stderr, "cannot configure Codex collection: %v\n", err)
		return 2
	}
	codexObserver, observerErr := codexobserver.New(codexobserver.Config{})
	if observerErr != nil {
		// Observation is optional and inferred. Do not include a path-bearing OS
		// error in logs, and never make official quota collection unavailable.
		fmt.Fprintln(stderr, "Codex session observation is unavailable")
		codexObserver = nil
	}
	config := companionruntime.Config{
		Version:        version,
		CodexCollector: codexCollector,
		CodexObserver:  codexObserver,
		Management: companionruntime.ManagementConfig{
			Address:       *managementAddress,
			AllowLAN:      *allowLANManagement,
			AllowedOrigin: *managementOrigin,
			AdminToken:    managementTokenValue,
		},
		DeviceHub: companionruntime.DeviceHubConfig{
			Address:        *deviceHubAddress,
			TLSCertificate: &tlsCertificate,
		},
		Pairing: pairingService,
	}
	if *headless {
		application, runtimeErr := companionruntime.New(config)
		if runtimeErr != nil {
			fmt.Fprintf(stderr, "cannot configure Companion: %v\n", runtimeErr)
			return 2
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if runtimeErr = application.Run(ctx); runtimeErr != nil {
			fmt.Fprintf(stderr, "Companion stopped with an error: %v\n", runtimeErr)
			return 1
		}
		return 0
	}
	controller, err := companionruntime.NewController(func() (*companionruntime.Runtime, error) {
		return companionruntime.New(config)
	})
	if err != nil {
		fmt.Fprintf(stderr, "cannot configure desktop lifecycle: %v\n", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err = controller.Start(); err != nil {
		fmt.Fprintf(stderr, "cannot start Companion runtime: %v\n", err)
		return 1
	}
	if controller.Status().State != companionruntime.StateReady {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		_ = controller.Stop(ctx)
		cancel()
		fmt.Fprintf(stderr, "Companion runtime did not become ready: %s\n", controller.Status().LastError)
		return 1
	}
	shell, err := desktop.NewShell(
		controller,
		desktop.NewNativeTray(),
		desktopassets.IconPNG(),
		desktop.OpenURL,
	)
	if err == nil {
		go func() {
			<-ctx.Done()
			shell.Close()
		}()
		err = shell.Run()
		shell.Close()
	}
	if err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		_ = controller.Stop(ctx)
		cancel()
		fmt.Fprintf(stderr, "desktop shell stopped with an error: %v\n", err)
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
