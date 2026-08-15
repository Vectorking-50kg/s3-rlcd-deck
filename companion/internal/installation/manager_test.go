package installation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
)

type fakePlatform struct {
	configured launchSpec
	installed  bool
	enabled    bool
	fail       bool
}

func (*fakePlatform) Name() string { return "fake" }
func (platform *fakePlatform) Configure(_ context.Context, spec launchSpec) error {
	if platform.fail {
		return ErrPlatform
	}
	platform.configured = spec
	platform.installed = true
	platform.enabled = false
	return nil
}
func (platform *fakePlatform) SetEnabled(_ context.Context, enabled bool) error {
	if platform.fail || !platform.installed {
		return ErrPlatform
	}
	platform.enabled = enabled
	return nil
}
func (platform *fakePlatform) Remove(context.Context) error {
	if platform.fail {
		return ErrPlatform
	}
	platform.configured = launchSpec{}
	platform.installed = false
	platform.enabled = false
	return nil
}
func (platform *fakePlatform) Status(context.Context) (platformStatus, error) {
	if platform.fail {
		return platformStatus{}, ErrPlatform
	}
	return platformStatus{Installed: platform.installed, Enabled: platform.enabled}, nil
}

func TestApplyUpgradeDisableAndUninstall(t *testing.T) {
	manager, platform, source := openTestManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, err := manager.Apply(ctx, Request{
		SourceExecutable: source, Version: "1.0.0", Commit: "0123456789ab",
		DeviceHubAddress: "127.0.0.1:7780",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed || !status.Enabled || status.Version != "1.0.0" ||
		status.PreviousVersion != "" || platform.configured.Executable != status.ActiveExecutable {
		t.Fatalf("unexpected install status: %#v", status)
	}
	if info, statErr := os.Stat(status.ActiveExecutable); statErr != nil || !info.Mode().IsRegular() {
		t.Fatalf("staged executable unavailable: %v", statErr)
	}
	if err = manager.SetEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status(ctx)
	if err != nil || status.Enabled {
		t.Fatalf("disabled status = %#v, %v", status, err)
	}
	if err = manager.SetEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}

	status, err = manager.Apply(ctx, Request{
		SourceExecutable: source, Version: "1.1.0", Commit: "abcdef012345",
		DeviceHubAddress: "0.0.0.0:7780",
	})
	if err != nil || status.PreviousVersion != "1.0.0" || status.Version != "1.1.0" {
		t.Fatalf("upgrade status = %#v, %v", status, err)
	}
	if err = manager.Uninstall(ctx); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status(ctx)
	if err != nil || status.Installed {
		t.Fatalf("uninstalled status = %#v, %v", status, err)
	}
	versions, err := os.ReadDir(filepath.Join(manager.root, "versions"))
	if err != nil || len(versions) != 2 {
		t.Fatalf("uninstall did not retain rollback executables: %d, %v", len(versions), err)
	}
}

func TestFailedMigrationRestoresDataAndPriorExecutable(t *testing.T) {
	manager, platform, source := openTestManager(t)
	ctx := context.Background()
	first, err := manager.Apply(ctx, Request{
		SourceExecutable: source, Version: "1.0.0", Commit: "0123456789ab",
		DeviceHubAddress: "127.0.0.1:7780",
	})
	if err != nil {
		t.Fatal(err)
	}
	dataPath := filepath.Join(manager.dataDirectory, "structured-providers.json")
	writePrivate(t, dataPath, []byte("old configuration"))
	manager.migrate = func(context.Context, string) error {
		writePrivate(t, dataPath, []byte("partially migrated"))
		return errors.New("schema failure")
	}
	if _, err = manager.Apply(ctx, Request{
		SourceExecutable: source, Version: "2.0.0", Commit: "abcdef012345",
		DeviceHubAddress: "127.0.0.1:7780",
	}); !errors.Is(err, ErrMigration) {
		t.Fatalf("migration failure = %v", err)
	}
	contents, err := protectedfile.Read(dataPath, 1024)
	if err != nil || string(contents) != "old configuration" {
		t.Fatalf("migration rollback contents = %q, %v", contents, err)
	}
	status, err := manager.Status(ctx)
	if err != nil || status.Version != "1.0.0" ||
		platform.configured.Executable != first.ActiveExecutable {
		t.Fatalf("prior executable was not restored: %#v, %v", status, err)
	}
}

func TestInterruptedTransactionJournalRecoversBeforeNextOperation(t *testing.T) {
	manager, platform, _ := openTestManager(t)
	dataPath := filepath.Join(manager.dataDirectory, "pairing.json")
	writePrivate(t, dataPath, []byte("prior trust"))
	backup, err := createMigrationSnapshot(
		context.Background(), manager.dataDirectory,
		filepath.Join(manager.root, "migration-backups"), manager.now(), "abcdef012345",
	)
	if err != nil {
		t.Fatal(err)
	}
	writePrivate(t, dataPath, []byte("partial trust"))
	prior := installationState{
		SchemaVersion: StateSchemaVersion, Version: "1.0.0", Commit: "0123456789ab",
		ActiveExecutable: filepath.Join(manager.root, "versions", "1.0.0-0123456789ab", "s3deck-companion"),
		DeviceHubAddress: "127.0.0.1:7780", Enabled: true,
	}
	next := prior
	next.Version = "2.0.0"
	next.Commit = "abcdef012345"
	next.ActiveExecutable = filepath.Join(manager.root, "versions", "2.0.0-abcdef012345", "s3deck-companion")
	journal := transactionJournal{
		SchemaVersion: StateSchemaVersion, RestoreData: true, Reconfigure: true,
		BackupPath: backup,
		HadPrior:   true, Prior: prior, Next: next,
	}
	if err = manager.writeJournal(journal); err != nil {
		t.Fatal(err)
	}
	platform.installed = true
	platform.enabled = true
	platform.configured = launchSpec{Executable: next.ActiveExecutable}
	if err = manager.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	contents, err := protectedfile.Read(dataPath, 1024)
	if err != nil || string(contents) != "prior trust" {
		t.Fatalf("journal recovery contents = %q, %v", contents, err)
	}
	if platform.configured.Executable != prior.ActiveExecutable {
		t.Fatal("journal recovery did not restore prior launch target")
	}
	if _, err = os.Lstat(manager.journalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("journal remained after successful recovery")
	}
}

func TestInterruptedDisableRestoresOnlyDesiredStartupState(t *testing.T) {
	manager, platform, source := openTestManager(t)
	stateStatus, err := manager.Apply(context.Background(), Request{
		SourceExecutable: source, Version: "1.0.0", Commit: "0123456789ab",
		DeviceHubAddress: "127.0.0.1:7780",
	})
	if err != nil {
		t.Fatal(err)
	}
	prior, found, err := manager.readState()
	if err != nil || !found {
		t.Fatal("installed state is unavailable")
	}
	next := prior
	next.Enabled = false
	if err = manager.writeJournal(transactionJournal{
		SchemaVersion: StateSchemaVersion,
		HadPrior:      true, Prior: prior, Next: next,
	}); err != nil {
		t.Fatal(err)
	}
	platform.enabled = false
	configuredBefore := platform.configured
	if err = manager.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !platform.enabled || platform.configured != configuredBefore ||
		platform.configured.Executable != stateStatus.ActiveExecutable {
		t.Fatal("disable recovery unnecessarily reconfigured the running application")
	}
}

func TestRejectsSymlinkSourceAndUnsafeListener(t *testing.T) {
	manager, _, source := openTestManager(t)
	symlink := filepath.Join(t.TempDir(), "companion-link")
	if err := os.Symlink(source, symlink); err != nil {
		t.Fatal(err)
	}
	for _, request := range []Request{
		{SourceExecutable: symlink, Version: "1.0.0", Commit: "0123456789ab", DeviceHubAddress: "127.0.0.1:7780"},
		{SourceExecutable: source, Version: "1.0.0", Commit: "0123456789ab", DeviceHubAddress: "255.255.255.255:7780"},
		{SourceExecutable: source, Version: "1.0.0", Commit: "0123456789ab", DeviceHubAddress: "240.0.0.1:7780"},
		{SourceExecutable: source, Version: "1.0.0", Commit: "0123456789ab", DeviceHubAddress: "host.example:7780"},
	} {
		if _, err := manager.Apply(context.Background(), request); err == nil {
			t.Fatalf("unsafe request accepted: %#v", request)
		}
	}
}

func TestStatusRejectsOrphanPlatformRegistration(t *testing.T) {
	manager, platform, _ := openTestManager(t)
	platform.installed = true
	platform.enabled = true
	if _, err := manager.Status(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("orphan registration status error = %v", err)
	}
}

func TestInsufficientSpaceFailsBeforeStagingOrMigration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	data := filepath.Join(t.TempDir(), "data")
	manager, err := Open(Config{
		RootDirectory: root, DataDirectory: data,
		AvailableBytes: func(string) (uint64, error) { return 0, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	manager.platform = &fakePlatform{}
	source := filepath.Join(t.TempDir(), "companion")
	if err = os.WriteFile(source, []byte("executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Apply(context.Background(), Request{
		SourceExecutable: source, Version: "1.0.0", Commit: "0123456789ab",
		DeviceHubAddress: "127.0.0.1:7780",
	}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("insufficient space error = %v", err)
	}
	if _, err = os.Lstat(filepath.Join(root, "versions")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("insufficient-space transaction staged an executable")
	}
}

func openTestManager(t *testing.T) (*Manager, *fakePlatform, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "安装 根")
	data := filepath.Join(t.TempDir(), "用户 数据")
	manager, err := Open(Config{
		RootDirectory: root, DataDirectory: data,
		Now: func() time.Time { return time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	platform := &fakePlatform{}
	manager.platform = platform
	manager.migrate = func(context.Context, string) error { return nil }
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close installation manager: %v", err)
		}
	})
	source := filepath.Join(t.TempDir(), "S3 Deck Companion")
	if err = os.WriteFile(source, []byte("test executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	return manager, platform, source
}

func writePrivate(t *testing.T, path string, contents []byte) {
	t.Helper()
	if _, err := protectedfile.Replace(path, contents); err != nil {
		t.Fatal(err)
	}
}
