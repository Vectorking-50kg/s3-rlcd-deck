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

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/backup"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/codexappserver"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/codexobserver"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/configmodel"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/cursorprovider"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/desktop"
	desktopassets "github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/desktop/assets"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/deviceidentity"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/history"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/managementtoken"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/pairing"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
	companionruntime "github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/runtime"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/secretstore"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/structuredprovider"
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
	backupExportPath := flags.String(
		"backup-export-file",
		"",
		"write one encrypted backup to an owner-only file and exit",
	)
	backupPassphraseFile := flags.String(
		"backup-passphrase-file",
		"",
		"read exact backup passphrase bytes from an owner-only file",
	)
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "s3deck-companion does not accept positional arguments")
		return 2
	}
	explicitFlags := make(map[string]bool)
	flags.Visit(func(visited *flag.Flag) { explicitFlags[visited.Name] = true })
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
	if *backupExportPath != "" || *backupPassphraseFile != "" {
		if *backupExportPath == "" || *backupPassphraseFile == "" ||
			!onlyBackupExportFlags(explicitFlags) {
			fmt.Fprintln(stderr, "backup export requires only --data-directory, --backup-export-file, and --backup-passphrase-file")
			return 2
		}
		_, backupService, _, _, closeConfiguration := loadStructuredProviders(
			resolvedDataDirectory,
			stderr,
		)
		defer closeConfiguration()
		if backupService == nil || exportBackupFile(
			backupService, *backupExportPath, *backupPassphraseFile,
		) != nil {
			fmt.Fprintln(stderr, "encrypted backup export failed")
			return 2
		}
		fmt.Fprintln(stdout, "encrypted backup exported")
		return 0
	}

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
	structuredProviderService, backupService, configurationOwner, restorableConfiguration,
		closeProviderDefinitions := loadStructuredProviders(
		resolvedDataDirectory,
		stderr,
	)
	defer closeProviderDefinitions()
	if configurationOwner != nil {
		webSettings := restorableConfiguration.WebSettings
		if explicitFlags["management-address"] {
			webSettings.ManagementAddress = *managementAddress
		} else {
			*managementAddress = webSettings.ManagementAddress
		}
		if explicitFlags["allow-lan-management"] {
			webSettings.AllowLAN = *allowLANManagement
		} else {
			*allowLANManagement = webSettings.AllowLAN
		}
		if explicitFlags["management-origin"] {
			webSettings.AllowedOrigin = *managementOrigin
		} else {
			*managementOrigin = webSettings.AllowedOrigin
		}
		if explicitFlags["management-address"] || explicitFlags["allow-lan-management"] ||
			explicitFlags["management-origin"] {
			settingsContext, cancelSettings := context.WithTimeout(context.Background(), 5*time.Second)
			settingsErr := configurationOwner.UpdateWebSettings(settingsContext, webSettings)
			cancelSettings()
			if settingsErr != nil {
				fmt.Fprintln(stderr, "management Web settings could not be persisted")
			}
		}
	}
	providerHistory, closeProviderHistory := loadProviderHistory(resolvedDataDirectory, stderr)
	defer closeProviderHistory()
	if configurationOwner != nil && providerHistory != nil {
		settingsContext, cancelSettings := context.WithTimeout(context.Background(), 5*time.Second)
		applicationSettings, pending, settingsErr := configurationOwner.PendingApplicationSettings(settingsContext)
		if settingsErr == nil && pending {
			settingsErr = providerHistory.SetEnabled(settingsContext, applicationSettings.HistoryEnabled)
			if settingsErr == nil {
				settingsErr = configurationOwner.UpdateApplicationSettings(settingsContext, applicationSettings)
			}
		} else if settingsErr == nil {
			currentSettings := configmodel.ApplicationSettings{HistoryEnabled: providerHistory.Enabled()}
			if currentSettings != applicationSettings {
				settingsErr = configurationOwner.UpdateApplicationSettings(
					settingsContext,
					currentSettings,
				)
			}
		}
		cancelSettings()
		if settingsErr != nil {
			fmt.Fprintln(stderr, "application settings synchronization is unavailable")
		}
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
	cursorCollector, err := cursorprovider.New(cursorprovider.Config{
		AdapterVersion:        cursorprovider.AdapterVersion,
		ResponseSchemaVersion: cursorprovider.ResponseSchemaVersion,
	})
	if err != nil {
		fmt.Fprintln(stderr, "Cursor usage collection is unavailable")
		cursorCollector = nil
	}
	config := companionruntime.Config{
		Version:             version,
		CodexCollector:      codexCollector,
		CodexObserver:       codexObserver,
		CursorCollector:     cursorCollector,
		StructuredProviders: structuredProviderService,
		History:             providerHistory,
		Backup:              backupService,
		Configuration:       configurationOwner,
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

type backupFileExporter interface {
	ExportFile(context.Context, string, []byte) error
}

func onlyBackupExportFlags(explicit map[string]bool) bool {
	for name := range explicit {
		switch name {
		case "data-directory", "backup-export-file", "backup-passphrase-file":
		default:
			return false
		}
	}
	return true
}

func exportBackupFile(exporter backupFileExporter, path, passphrasePath string) error {
	if exporter == nil || path == "" || passphrasePath == "" {
		return backup.ErrFile
	}
	passphrase, err := protectedfile.Read(passphrasePath, 1024)
	if err != nil {
		return backup.ErrFile
	}
	defer clear(passphrase)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return exporter.ExportFile(ctx, path, passphrase)
}

func loadProviderHistory(dataDirectory string, stderr io.Writer) (*history.Store, func()) {
	openContext, cancelOpen := context.WithTimeout(context.Background(), 5*time.Second)
	store, err := history.Open(openContext, history.Config{
		Path: filepath.Join(dataDirectory, "provider-history.sqlite3"),
	})
	cancelOpen()
	if err != nil {
		// Database paths and SQLite diagnostics may contain private filesystem
		// context. History is non-critical and degrades behind a fixed message.
		fmt.Fprintln(stderr, "Provider history is unavailable")
		return nil, func() {}
	}
	return store, func() {
		closeContext, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
		_ = store.Close(closeContext)
		cancelClose()
	}
}

func defaultDataDirectory() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return "", errors.Join(errors.New("user configuration directory is unavailable"), err)
	}
	return filepath.Join(base, "s3-rlcd-deck"), nil
}

func loadStructuredProviders(
	dataDirectory string,
	stderr io.Writer,
) (
	*structuredprovider.Service,
	*backup.Service,
	*structuredprovider.ConfigurationStore,
	structuredprovider.RestorableConfiguration,
	func(),
) {
	closeStore := func() {}
	providerSecrets, err := secretstore.OpenForDataDirectory(dataDirectory)
	if err != nil {
		fmt.Fprintln(stderr, "structured Providers are unavailable")
		return nil, nil, nil, structuredprovider.RestorableConfiguration{}, closeStore
	}
	providerDefinitions, err := structuredprovider.OpenConfigurationStore(
		filepath.Join(dataDirectory, "structured-providers.json"),
	)
	if err != nil {
		fmt.Fprintln(stderr, "structured Provider configuration is unavailable")
		return nil, nil, nil, structuredprovider.RestorableConfiguration{}, closeStore
	}
	closeStore = func() { _ = providerDefinitions.Close() }
	cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
	cleanupErr := providerDefinitions.ReconcileCleanup(cleanupContext, providerSecrets)
	cancelCleanup()
	if cleanupErr != nil {
		// References are non-secret, but even they do not belong in logs. The
		// protected journal remains authoritative for the next startup retry.
		fmt.Fprintln(stderr, "structured Provider secret cleanup remains pending")
	}
	restorableConfiguration, err := providerDefinitions.Configuration(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, "structured Provider configuration is unavailable")
		return nil, nil, nil, structuredprovider.RestorableConfiguration{}, closeStore
	}
	backupService, err := backup.NewService(providerDefinitions, providerSecrets, nil)
	if err != nil {
		fmt.Fprintln(stderr, "encrypted backup service is unavailable")
		backupService = nil
	}
	providerService, err := structuredprovider.NewService(providerDefinitions, providerSecrets)
	if err != nil {
		fmt.Fprintln(stderr, "structured Provider management is unavailable")
		providerService = nil
	}
	return providerService, backupService, providerDefinitions, restorableConfiguration, closeStore
}
