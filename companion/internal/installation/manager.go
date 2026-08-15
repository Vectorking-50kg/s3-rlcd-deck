package installation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
)

const (
	maximumExecutableBytes = 256 << 20
	stateFileName          = "installation.json"
	journalFileName        = "installation-journal.json"
	installLockName        = ".installation.lock"
)

var safeIdentity = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)
var safeCommit = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

type Manager struct {
	mutex          sync.Mutex
	root           string
	dataDirectory  string
	now            func() time.Time
	platform       platformAdapter
	lock           *protectedfile.Lock
	migrate        func(context.Context, string) error
	availableBytes func(string) (uint64, error)
}

func Open(config Config) (*Manager, error) {
	return openManager(config, true)
}

// OpenWithoutRecovery opens a stable installation for status and startup
// registration changes that are safe while the Companion is running. It
// refuses an interrupted transaction instead of restoring live data.
func OpenWithoutRecovery(config Config) (*Manager, error) {
	return openManager(config, false)
}

func openManager(config Config, recoverInterrupted bool) (*Manager, error) {
	root, err := filepath.Abs(filepath.Clean(config.RootDirectory))
	if err != nil || root == "" || config.RootDirectory == "" {
		return nil, ErrInvalid
	}
	dataDirectory, err := filepath.Abs(filepath.Clean(config.DataDirectory))
	if err != nil || dataDirectory == "" || config.DataDirectory == "" {
		return nil, ErrInvalid
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.AvailableBytes == nil {
		config.AvailableBytes = diskAvailableBytes
	}
	root, err = prepareCanonicalPrivateDirectory(root)
	if err != nil {
		return nil, fmt.Errorf("%w: prepare installation root", ErrUnavailable)
	}
	dataDirectory, err = prepareCanonicalPrivateDirectory(dataDirectory)
	if err != nil {
		return nil, fmt.Errorf("%w: prepare data directory", ErrUnavailable)
	}
	lock, err := protectedfile.AcquireDirectoryLock(root, installLockName)
	if err != nil {
		return nil, fmt.Errorf("%w: acquire installation lock", ErrUnavailable)
	}
	platform := config.platform
	if platform == nil {
		platform = newPlatformAdapter(root)
	}
	manager := &Manager{
		root: root, dataDirectory: dataDirectory, now: config.Now,
		platform: platform, lock: lock, migrate: migrateData,
		availableBytes: config.AvailableBytes,
	}
	if manager.platform == nil {
		_ = lock.Close()
		return nil, ErrUnavailable
	}
	if !recoverInterrupted {
		var required bool
		required, err = manager.recoveryRequired()
		if err == nil && required {
			err = ErrRecoveryRequired
		}
	} else {
		recoveryContext, cancelRecovery := context.WithTimeout(context.Background(), 30*time.Second)
		err = manager.recover(recoveryContext)
		cancelRecovery()
	}
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	return manager, nil
}

func (manager *Manager) recoveryRequired() (bool, error) {
	_, err := os.Lstat(manager.journalPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: inspect installation journal", ErrUnavailable)
	}
	// Any object at the journal path requires fenced recovery. Open() will
	// validate its type, permissions, schema, and contents after that fence is
	// held; this read-only probe must never mutate live data.
	return true, nil
}

func (manager *Manager) Close() error {
	if manager == nil {
		return nil
	}
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	if manager.lock == nil {
		return nil
	}
	err := manager.lock.Close()
	manager.lock = nil
	return err
}

func (manager *Manager) Apply(ctx context.Context, request Request) (
	status Status,
	resultErr error,
) {
	if manager == nil || ctx == nil {
		return Status{}, ErrInvalid
	}
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	if manager.lock == nil || ctx.Err() != nil || !validRequest(request) {
		return Status{}, errors.Join(ErrInvalid, ctx.Err())
	}
	prior, hadPrior, err := manager.readState()
	if err != nil {
		return Status{}, err
	}
	if err = manager.preflightCapacity(request.SourceExecutable); err != nil {
		return Status{}, err
	}
	target, err := manager.stageExecutable(request)
	if err != nil {
		return Status{}, err
	}
	backupPath, err := createMigrationSnapshot(
		ctx,
		manager.dataDirectory,
		filepath.Join(manager.root, "migration-backups"),
		manager.now().UTC(),
		request.Commit,
	)
	if err != nil {
		return Status{}, errors.Join(ErrMigration, err)
	}
	next := installationState{
		SchemaVersion: StateSchemaVersion, Version: request.Version, Commit: request.Commit,
		ActiveExecutable: target, DeviceHubAddress: request.DeviceHubAddress, Enabled: false,
	}
	if hadPrior {
		next.PreviousVersion = prior.Version
		next.PreviousExecutable = prior.ActiveExecutable
	}
	journal := transactionJournal{
		SchemaVersion: StateSchemaVersion, RestoreData: true, Reconfigure: true,
		BackupPath: backupPath,
		HadPrior:   hadPrior, Prior: prior, Next: next,
	}
	if err = manager.writeJournal(journal); err != nil {
		return Status{}, err
	}
	committed := false
	defer func() {
		if !committed {
			resultErr = errors.Join(resultErr, manager.rollbackBounded(journal))
		}
	}()
	if err = manager.migrate(ctx, manager.dataDirectory); err != nil {
		return Status{}, errors.Join(ErrMigration, err)
	}
	if err = manager.platform.Configure(ctx, launchSpec{
		Executable: target, DataDirectory: manager.dataDirectory,
		DeviceHubAddress: request.DeviceHubAddress,
	}); err != nil {
		return Status{}, errors.Join(ErrPlatform, err)
	}
	if err = manager.writeState(next); err != nil {
		return Status{}, err
	}
	if err = manager.platform.SetEnabled(ctx, true); err != nil {
		return Status{}, errors.Join(ErrPlatform, err)
	}
	next.Enabled = true
	if err = manager.writeState(next); err != nil {
		return Status{}, err
	}
	if err = os.Remove(manager.journalPath()); err != nil {
		return Status{}, fmt.Errorf("%w: commit installation journal", ErrUnavailable)
	}
	committed = true
	return manager.statusFromState(ctx, next, true)
}

func (manager *Manager) SetEnabled(ctx context.Context, enabled bool) (resultErr error) {
	if manager == nil || ctx == nil {
		return ErrInvalid
	}
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	state, found, err := manager.readState()
	if err != nil || !found {
		return errors.Join(ErrUnavailable, err)
	}
	if state.Enabled == enabled {
		_, statusErr := manager.statusFromState(ctx, state, true)
		return statusErr
	}
	next := state
	next.Enabled = enabled
	journal := transactionJournal{
		SchemaVersion: StateSchemaVersion,
		HadPrior:      true, Prior: state, Next: next,
	}
	if err = manager.writeJournal(journal); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			resultErr = errors.Join(resultErr, manager.rollbackBounded(journal))
		}
	}()
	if err = manager.platform.SetEnabled(ctx, enabled); err != nil {
		return errors.Join(ErrPlatform, err)
	}
	if err = manager.writeState(next); err != nil {
		return err
	}
	if err = os.Remove(manager.journalPath()); err != nil {
		return fmt.Errorf("%w: commit startup state", ErrUnavailable)
	}
	committed = true
	return nil
}

func (manager *Manager) Uninstall(ctx context.Context) (resultErr error) {
	if manager == nil || ctx == nil {
		return ErrInvalid
	}
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	state, found, err := manager.readState()
	if err != nil {
		return err
	}
	if !found {
		if err = manager.platform.Remove(ctx); err != nil {
			return errors.Join(ErrPlatform, err)
		}
		return nil
	}
	journal := transactionJournal{
		SchemaVersion: StateSchemaVersion, Reconfigure: true,
		HadPrior: true, Prior: state, Next: state,
	}
	if err = manager.writeJournal(journal); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			resultErr = errors.Join(resultErr, manager.rollbackBounded(journal))
		}
	}()
	if err := manager.platform.Remove(ctx); err != nil {
		return errors.Join(ErrPlatform, err)
	}
	if err := os.Remove(manager.statePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: remove installation state", ErrUnavailable)
	}
	if err = os.Remove(manager.journalPath()); err != nil {
		return fmt.Errorf("%w: commit uninstall journal", ErrUnavailable)
	}
	committed = true
	// User data, migration snapshots, and old executables are deliberately
	// retained. Removing credentials/data is a separate explicit product action.
	return nil
}

func (manager *Manager) Status(ctx context.Context) (Status, error) {
	if manager == nil || ctx == nil {
		return Status{}, ErrInvalid
	}
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	state, found, err := manager.readState()
	if err != nil {
		return Status{Platform: manager.platform.Name()}, err
	}
	if !found {
		platform, platformErr := manager.platform.Status(ctx)
		if platformErr != nil {
			return Status{Platform: manager.platform.Name()}, errors.Join(ErrPlatform, platformErr)
		}
		if platform.Installed {
			return Status{Platform: manager.platform.Name()}, fmt.Errorf(
				"%w: login startup exists without installation state", ErrUnavailable,
			)
		}
		return Status{Platform: manager.platform.Name()}, nil
	}
	return manager.statusFromState(ctx, state, found)
}

func (manager *Manager) statusFromState(
	ctx context.Context,
	state installationState,
	found bool,
) (Status, error) {
	platform, err := manager.platform.Status(ctx)
	if err != nil {
		return Status{}, errors.Join(ErrPlatform, err)
	}
	if found && (!platform.Installed || platform.Enabled != state.Enabled) {
		return Status{}, fmt.Errorf("%w: installation state does not match login startup", ErrUnavailable)
	}
	return Status{
		Installed: found && platform.Installed, Enabled: found && platform.Enabled,
		Version: state.Version, Commit: state.Commit,
		ActiveExecutable: state.ActiveExecutable, PreviousVersion: state.PreviousVersion,
		Platform: manager.platform.Name(),
	}, nil
}

func (manager *Manager) recover(ctx context.Context) error {
	document, err := protectedfile.Read(manager.journalPath(), 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("%w: read installation journal", ErrUnavailable)
	}
	defer clear(document)
	var journal transactionJournal
	if decodeStrict(document, &journal) != nil || !validJournal(journal, manager.root) {
		return fmt.Errorf("%w: invalid installation journal", ErrUnavailable)
	}
	return manager.rollback(ctx, journal)
}

func (manager *Manager) rollbackBounded(journal transactionJournal) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return manager.rollback(ctx, journal)
}

func (manager *Manager) rollback(ctx context.Context, journal transactionJournal) error {
	var result error
	if journal.RestoreData {
		if err := restoreMigrationSnapshot(ctx, manager.dataDirectory, journal.BackupPath); err != nil {
			result = errors.Join(result, errors.Join(ErrMigration, err))
		}
	}
	if journal.HadPrior {
		var err error
		if journal.Reconfigure {
			err = manager.platform.Configure(ctx, launchSpec{
				Executable:       journal.Prior.ActiveExecutable,
				DataDirectory:    manager.dataDirectory,
				DeviceHubAddress: journal.Prior.DeviceHubAddress,
			})
		}
		if err == nil {
			err = manager.platform.SetEnabled(ctx, journal.Prior.Enabled)
		}
		if err != nil {
			result = errors.Join(result, errors.Join(ErrPlatform, err))
		} else if err = manager.writeState(journal.Prior); err != nil {
			result = errors.Join(result, err)
		}
	} else {
		if err := manager.platform.Remove(ctx); err != nil {
			result = errors.Join(result, errors.Join(ErrPlatform, err))
		}
		if err := os.Remove(manager.statePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}
	if result == nil {
		if err := os.Remove(manager.journalPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return result
}

func (manager *Manager) stageExecutable(request Request) (string, error) {
	source, err := os.Open(request.SourceExecutable)
	if err != nil {
		return "", fmt.Errorf("%w: open source executable", ErrUnavailable)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumExecutableBytes {
		return "", fmt.Errorf("%w: source executable is invalid", ErrInvalid)
	}
	pathInfo, err := os.Lstat(request.SourceExecutable)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, pathInfo) {
		return "", fmt.Errorf("%w: source executable identity is unstable", ErrInvalid)
	}
	contents, err := io.ReadAll(io.LimitReader(source, maximumExecutableBytes+1))
	if err != nil || len(contents) == 0 || len(contents) > maximumExecutableBytes {
		clear(contents)
		return "", fmt.Errorf("%w: read source executable", ErrUnavailable)
	}
	defer clear(contents)
	afterRead, err := source.Stat()
	currentPath, pathErr := os.Lstat(request.SourceExecutable)
	if err != nil || pathErr != nil || !os.SameFile(info, afterRead) ||
		!os.SameFile(afterRead, currentPath) || afterRead.Size() != int64(len(contents)) ||
		afterRead.ModTime() != info.ModTime() {
		return "", fmt.Errorf("%w: source executable changed while staging", ErrInvalid)
	}
	directory := filepath.Join(manager.root, "versions", request.Version+"-"+request.Commit)
	if err = protectedfile.EnsurePrivateDirectory(directory); err != nil {
		return "", fmt.Errorf("%w: create version directory", ErrUnavailable)
	}
	name := "s3deck-companion"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(directory, name)
	sourcePath, absoluteErr := filepath.Abs(filepath.Clean(request.SourceExecutable))
	if absoluteErr != nil {
		return "", fmt.Errorf("%w: resolve source executable", ErrInvalid)
	}
	if canonicalSource, canonicalErr := filepath.EvalSymlinks(sourcePath); canonicalErr == nil {
		sourcePath = canonicalSource
	}
	samePath := filepath.Clean(sourcePath) == filepath.Clean(target)
	if runtime.GOOS == "windows" {
		samePath = strings.EqualFold(sourcePath, target)
	}
	if !samePath {
		if _, err = protectedfile.Replace(target, contents); err != nil {
			return "", fmt.Errorf("%w: stage executable", ErrUnavailable)
		}
	}
	if runtime.GOOS != "windows" {
		if err = os.Chmod(target, 0o700); err != nil {
			return "", fmt.Errorf("%w: protect executable", ErrUnavailable)
		}
	}
	targetInfo, err := os.Lstat(target)
	if err != nil || !targetInfo.Mode().IsRegular() || targetInfo.Mode()&os.ModeSymlink != 0 ||
		runtime.GOOS != "windows" && targetInfo.Mode().Perm() != 0o700 ||
		runtime.GOOS == "windows" && protectedfile.VerifyPrivate(target) != nil {
		return "", fmt.Errorf("%w: verify executable protection", ErrUnavailable)
	}
	verified, err := os.ReadFile(target)
	if err != nil {
		return "", fmt.Errorf("%w: verify executable", ErrUnavailable)
	}
	want := sha256.Sum256(contents)
	got := sha256.Sum256(verified)
	clear(verified)
	if want != got {
		return "", fmt.Errorf("%w: executable hash mismatch", ErrUnavailable)
	}
	return target, nil
}

func prepareCanonicalPrivateDirectory(path string) (string, error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	canonical, err = filepath.Abs(filepath.Clean(canonical))
	if err != nil {
		return "", err
	}
	if err = protectedfile.EnsurePrivateDirectory(canonical); err != nil {
		return "", err
	}
	return canonical, nil
}

func validRequest(request Request) bool {
	if request.SourceExecutable == "" || !safeIdentity.MatchString(request.Version) ||
		!safeCommit.MatchString(request.Commit) {
		return false
	}
	return validDeviceHubListenAddress(request.DeviceHubAddress)
}

func (manager *Manager) preflightCapacity(sourcePath string) error {
	source, err := os.Lstat(sourcePath)
	if err != nil || !source.Mode().IsRegular() || source.Mode()&os.ModeSymlink != 0 ||
		source.Size() <= 0 || source.Size() > maximumExecutableBytes {
		return fmt.Errorf("%w: source executable is invalid", ErrInvalid)
	}
	var migrationBytes uint64
	for _, name := range migrationFiles {
		info, statErr := os.Lstat(filepath.Join(manager.dataDirectory, name))
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
			info.Size() < 0 || info.Size() > maximumMigrationFileBytes {
			return fmt.Errorf("%w: migration input is unavailable", ErrMigration)
		}
		migrationBytes += uint64(info.Size())
	}
	const transactionReserve = uint64(16 << 20)
	rootNeed := uint64(source.Size()) + migrationBytes + transactionReserve
	dataNeed := migrationBytes + transactionReserve
	combinedNeed := rootNeed + dataNeed
	rootAvailable, err := manager.availableBytes(manager.root)
	if err != nil || rootAvailable < combinedNeed {
		return fmt.Errorf("%w: insufficient installation space", ErrUnavailable)
	}
	dataAvailable, err := manager.availableBytes(manager.dataDirectory)
	if err != nil || dataAvailable < combinedNeed {
		return fmt.Errorf("%w: insufficient migration space", ErrUnavailable)
	}
	return nil
}

func (manager *Manager) statePath() string   { return filepath.Join(manager.root, stateFileName) }
func (manager *Manager) journalPath() string { return filepath.Join(manager.root, journalFileName) }

func (manager *Manager) readState() (installationState, bool, error) {
	document, err := protectedfile.Read(manager.statePath(), 64<<10)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return installationState{}, false, nil
		}
		return installationState{}, false, fmt.Errorf("%w: read installation state", ErrUnavailable)
	}
	defer clear(document)
	var state installationState
	if decodeStrict(document, &state) != nil || !validState(state, manager.root) {
		return installationState{}, false, fmt.Errorf("%w: invalid installation state", ErrUnavailable)
	}
	return state, true, nil
}

func (manager *Manager) writeState(state installationState) error {
	return writePrivateJSON(manager.statePath(), state)
}

func (manager *Manager) writeJournal(journal transactionJournal) error {
	return writePrivateJSON(manager.journalPath(), journal)
}

func writePrivateJSON(path string, value any) error {
	document, err := json.Marshal(value)
	if err != nil {
		return err
	}
	document = append(document, '\n')
	defer clear(document)
	if _, err = protectedfile.Replace(path, document); err != nil {
		return fmt.Errorf("%w: commit protected installation state", ErrUnavailable)
	}
	return nil
}

func decodeStrict(document []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(document)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ErrInvalid
	}
	return nil
}

func validState(state installationState, root string) bool {
	if state.SchemaVersion != StateSchemaVersion || !safeIdentity.MatchString(state.Version) ||
		!safeCommit.MatchString(state.Commit) || !filepath.IsAbs(state.ActiveExecutable) ||
		!pathWithin(root, state.ActiveExecutable) {
		return false
	}
	if state.PreviousExecutable != "" &&
		(!filepath.IsAbs(state.PreviousExecutable) || !pathWithin(root, state.PreviousExecutable) ||
			!safeIdentity.MatchString(state.PreviousVersion)) {
		return false
	}
	return validDeviceHubListenAddress(state.DeviceHubAddress)
}

func validDeviceHubListenAddress(value string) bool {
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return false
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	ip := net.ParseIP(host)
	ipv4 := ip.To4()
	if err != nil || port == 0 || ip == nil || ipv4 == nil || ip.IsMulticast() {
		return false
	}
	if ip.Equal(net.IPv4bcast) || ipv4[0] >= 224 || ipv4[0] == 0 && !ip.IsUnspecified() {
		return false
	}
	return true
}

func validJournal(journal transactionJournal, root string) bool {
	if journal.SchemaVersion != StateSchemaVersion || !validState(journal.Next, root) ||
		journal.RestoreData && !journal.Reconfigure {
		return false
	}
	if journal.RestoreData {
		if !filepath.IsAbs(journal.BackupPath) ||
			!pathWithin(filepath.Join(root, "migration-backups"), journal.BackupPath) {
			return false
		}
	} else if journal.BackupPath != "" {
		return false
	}
	return !journal.HadPrior || validState(journal.Prior, root)
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
