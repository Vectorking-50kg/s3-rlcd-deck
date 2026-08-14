//go:build windows

package secretstore

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	credentialTypeGeneric         = 1
	credentialPersistLocalMachine = 2
	credentialMutexWaitMillis     = 50
	credentialMutexName           = `Local\Vectorking.S3RLCDDeck.Companion.ProviderSecretStore.v1`
)

var (
	credentialDLL       = windows.NewLazySystemDLL("advapi32.dll")
	credentialRead      = credentialDLL.NewProc("CredReadW")
	credentialWrite     = credentialDLL.NewProc("CredWriteW")
	credentialDelete    = credentialDLL.NewProc("CredDeleteW")
	credentialEnumerate = credentialDLL.NewProc("CredEnumerateW")
	credentialFree      = credentialDLL.NewProc("CredFree")
)

type nativeCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

type windowsVault struct {
	service string
}

func platformVault(service string) (vault, error) {
	if service == "" {
		return nil, ErrInvalid
	}
	return &windowsVault{service: service}, nil
}

func (vault *windowsVault) Create(ctx context.Context, account string, secret []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mutationLock, err := acquireWindowsMutationLock(ctx)
	if err != nil {
		return err
	}
	defer mutationLock.release()
	existing, err := vault.read(account)
	if err == nil {
		overwrite(existing)
		return ErrDuplicate
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	return vault.write(ctx, account, secret)
}

func (vault *windowsVault) Update(ctx context.Context, account string, secret []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	existing, err := vault.read(account)
	overwrite(existing)
	if err != nil {
		return err
	}
	return vault.write(ctx, account, secret)
}

func (vault *windowsVault) Get(ctx context.Context, account string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	secret, err := vault.read(account)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		overwrite(secret)
		return nil, err
	}
	return secret, nil
}

func (vault *windowsVault) Delete(ctx context.Context, account string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(vault.target(account))
	if err != nil {
		return ErrInvalid
	}
	result, _, callErr := credentialDelete.Call(
		uintptr(unsafe.Pointer(target)), credentialTypeGeneric, 0,
	)
	runtime.KeepAlive(target)
	if result == 0 {
		return windowsVaultError(callErr)
	}
	return nil
}

func (vault *windowsVault) List(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	filter, err := windows.UTF16PtrFromString(vault.service + ":*")
	if err != nil {
		return nil, ErrInvalid
	}
	var count uint32
	var credentials **nativeCredential
	result, _, callErr := credentialEnumerate.Call(
		uintptr(unsafe.Pointer(filter)), 0,
		uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&credentials)),
	)
	runtime.KeepAlive(filter)
	if result == 0 {
		mapped := windowsVaultError(callErr)
		if errors.Is(mapped, ErrNotFound) {
			return []string{}, nil
		}
		return nil, mapped
	}
	defer credentialFree.Call(uintptr(unsafe.Pointer(credentials)))
	if count == 0 {
		return []string{}, nil
	}
	if credentials == nil {
		return nil, ErrCorrupt
	}
	items := unsafe.Slice(credentials, int(count))
	// CredEnumerateW has no metadata-only mode and materializes each matching
	// blob in its native result block. Register the wipe before validating the
	// count so even an oversized/corrupt native result is cleared before free.
	defer overwriteCredentialBlobs(items)
	if count > maximumMetadata {
		return nil, ErrCorrupt
	}
	prefix := vault.service + ":"
	accounts := make([]string, 0, len(items))
	for _, credential := range items {
		if credential == nil || credential.Type != credentialTypeGeneric || credential.TargetName == nil {
			return nil, ErrCorrupt
		}
		target := windows.UTF16PtrToString(credential.TargetName)
		if !strings.HasPrefix(target, prefix) {
			return nil, ErrCorrupt
		}
		accounts = append(accounts, strings.TrimPrefix(target, prefix))
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return accounts, nil
}

type windowsMutationLock struct {
	handle windows.Handle
}

func acquireWindowsMutationLock(ctx context.Context) (*windowsMutationLock, error) {
	name, err := windows.UTF16PtrFromString(credentialMutexName)
	if err != nil {
		return nil, ErrUnavailable
	}
	handle, err := windows.CreateMutex(nil, false, name)
	runtime.KeepAlive(name)
	// x/sys/windows intentionally returns ERROR_ALREADY_EXISTS together with
	// the valid handle for CreateMutexW. That is the normal contended path, not
	// a creation failure; the caller must wait on and later close that handle.
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, windowsVaultError(err)
	}
	runtime.LockOSThread()
	for {
		if err = ctx.Err(); err != nil {
			runtime.UnlockOSThread()
			_ = windows.CloseHandle(handle)
			return nil, err
		}
		result, waitErr := windows.WaitForSingleObject(handle, credentialMutexWaitMillis)
		if waitErr != nil {
			runtime.UnlockOSThread()
			_ = windows.CloseHandle(handle)
			return nil, windowsVaultError(waitErr)
		}
		switch result {
		case windows.WAIT_OBJECT_0, windows.WAIT_ABANDONED:
			return &windowsMutationLock{handle: handle}, nil
		case uint32(windows.WAIT_TIMEOUT):
			continue
		default:
			runtime.UnlockOSThread()
			_ = windows.CloseHandle(handle)
			return nil, ErrUnavailable
		}
	}
}

func (lock *windowsMutationLock) release() {
	if lock == nil || lock.handle == 0 {
		return
	}
	_ = windows.ReleaseMutex(lock.handle)
	_ = windows.CloseHandle(lock.handle)
	lock.handle = 0
	runtime.UnlockOSThread()
}

func overwriteCredentialBlobs(credentials []*nativeCredential) {
	for _, credential := range credentials {
		if credential == nil || credential.CredentialBlob == nil ||
			credential.CredentialBlobSize == 0 {
			continue
		}
		// Credential Manager guarantees this bound for generic credentials.
		if credential.CredentialBlobSize <= maximumSecretBytes {
			overwrite(unsafe.Slice(
				credential.CredentialBlob,
				int(credential.CredentialBlobSize),
			))
		}
	}
}

func (vault *windowsVault) read(account string) ([]byte, error) {
	target, err := windows.UTF16PtrFromString(vault.target(account))
	if err != nil {
		return nil, ErrInvalid
	}
	var credential *nativeCredential
	result, _, callErr := credentialRead.Call(
		uintptr(unsafe.Pointer(target)), credentialTypeGeneric, 0,
		uintptr(unsafe.Pointer(&credential)),
	)
	runtime.KeepAlive(target)
	if result == 0 {
		return nil, windowsVaultError(callErr)
	}
	defer credentialFree.Call(uintptr(unsafe.Pointer(credential)))
	if credential == nil || credential.CredentialBlob == nil ||
		credential.CredentialBlobSize == 0 || credential.CredentialBlobSize > maximumSecretBytes {
		return nil, ErrCorrupt
	}
	secret := unsafe.Slice(credential.CredentialBlob, int(credential.CredentialBlobSize))
	return append([]byte(nil), secret...), nil
}

func (vault *windowsVault) write(ctx context.Context, account string, secret []byte) error {
	target, err := windows.UTF16PtrFromString(vault.target(account))
	if err != nil {
		return ErrInvalid
	}
	user, err := windows.UTF16PtrFromString("local-user")
	if err != nil {
		return ErrInvalid
	}
	owned := append([]byte(nil), secret...)
	defer overwrite(owned)
	credential := nativeCredential{
		Type:               credentialTypeGeneric,
		TargetName:         target,
		CredentialBlobSize: uint32(len(owned)),
		CredentialBlob:     &owned[0],
		Persist:            credentialPersistLocalMachine,
		UserName:           user,
	}
	result, _, callErr := credentialWrite.Call(uintptr(unsafe.Pointer(&credential)), 0)
	runtime.KeepAlive(target)
	runtime.KeepAlive(user)
	runtime.KeepAlive(owned)
	if result == 0 {
		return windowsVaultError(callErr)
	}
	return nil
}

func (vault *windowsVault) target(account string) string {
	return vault.service + ":" + account
}

func windowsVaultError(err error) error {
	switch {
	case errors.Is(err, windows.ERROR_NOT_FOUND):
		return ErrNotFound
	case errors.Is(err, windows.ERROR_CANCELLED):
		return ErrCanceled
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return ErrPermission
	case errors.Is(err, windows.ERROR_NO_SUCH_LOGON_SESSION):
		return ErrLocked
	default:
		return ErrUnavailable
	}
}
