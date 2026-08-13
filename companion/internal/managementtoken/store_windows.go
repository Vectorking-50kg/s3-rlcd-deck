//go:build windows

package managementtoken

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

const (
	credentialTypeGeneric         = 1
	credentialPersistLocalMachine = 2
	errorNotFound                 = syscall.Errno(1168)
)

var (
	advapi32   = syscall.NewLazyDLL("advapi32.dll")
	credReadW  = advapi32.NewProc("CredReadW")
	credWriteW = advapi32.NewProc("CredWriteW")
	credFree   = advapi32.NewProc("CredFree")
)

type windowsCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        syscall.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

type windowsSecretStore struct{}

func platformSecretStore() secretStore { return windowsSecretStore{} }

func credentialTarget(service string, account string) (*uint16, error) {
	return syscall.UTF16PtrFromString(service + ":" + account)
}

func (windowsSecretStore) Get(service string, account string) (string, error) {
	target, err := credentialTarget(service, account)
	if err != nil {
		return "", err
	}
	var credential *windowsCredential
	result, _, callErr := credReadW.Call(
		uintptr(unsafe.Pointer(target)), credentialTypeGeneric, 0,
		uintptr(unsafe.Pointer(&credential)),
	)
	if result == 0 {
		if errors.Is(callErr, errorNotFound) {
			return "", ErrSecretNotFound
		}
		return "", fmt.Errorf("read Windows Credential Manager item: %w", callErr)
	}
	defer credFree.Call(uintptr(unsafe.Pointer(credential)))
	if credential.CredentialBlobSize == 0 || credential.CredentialBlob == nil {
		return "", errors.New("Windows Credential Manager item is empty")
	}
	secret := unsafe.Slice(credential.CredentialBlob, credential.CredentialBlobSize)
	return string(secret), nil
}

func (windowsSecretStore) Set(service string, account string, secret string) error {
	target, err := credentialTarget(service, account)
	if err != nil {
		return err
	}
	user, err := syscall.UTF16PtrFromString("local-user")
	if err != nil {
		return err
	}
	blob := []byte(secret)
	credential := windowsCredential{
		Type:               credentialTypeGeneric,
		TargetName:         target,
		CredentialBlobSize: uint32(len(blob)),
		CredentialBlob:     &blob[0],
		Persist:            credentialPersistLocalMachine,
		UserName:           user,
	}
	result, _, callErr := credWriteW.Call(uintptr(unsafe.Pointer(&credential)), 0)
	if result == 0 {
		return fmt.Errorf("write Windows Credential Manager item: %w", callErr)
	}
	return nil
}
