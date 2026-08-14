//go:build windows

package secretstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsVaultErrorMappingIsStable(t *testing.T) {
	tests := []struct {
		status error
		want   error
	}{
		{windows.ERROR_NOT_FOUND, ErrNotFound},
		{windows.ERROR_CANCELLED, ErrCanceled},
		{windows.ERROR_ACCESS_DENIED, ErrPermission},
		{windows.ERROR_NO_SUCH_LOGON_SESSION, ErrLocked},
		{windows.ERROR_INVALID_DATA, ErrUnavailable},
	}
	for _, testCase := range tests {
		if err := windowsVaultError(testCase.status); !errors.Is(err, testCase.want) {
			t.Fatalf("windowsVaultError(%v) = %v, want %v", testCase.status, err, testCase.want)
		}
	}
}

func TestWindowsEnumerationBlobCleanup(t *testing.T) {
	first := []byte("PRIVATE_WINDOWS_ENUMERATION_CANARY")
	second := []byte("SECOND_PRIVATE_WINDOWS_ENUMERATION_CANARY")
	credentials := []*nativeCredential{
		{CredentialBlobSize: uint32(len(first)), CredentialBlob: &first[0]},
		nil,
		{CredentialBlobSize: uint32(len(second)), CredentialBlob: &second[0]},
	}
	overwriteCredentialBlobs(credentials)
	if !bytes.Equal(first, make([]byte, len(first))) ||
		!bytes.Equal(second, make([]byte, len(second))) {
		t.Fatal("enumerated Credential Manager blobs were not overwritten")
	}
}

func TestNativeWindowsCreateIsDuplicateSafeAcrossStores(t *testing.T) {
	if os.Getenv("S3DECK_NATIVE_SECRET_STORE_TEST") != "1" {
		t.Skip("native Credential Manager test is disabled")
	}
	first, err := platformVault(providerSecretService)
	if err != nil {
		t.Fatal(err)
	}
	second, err := platformVault(providerSecretService)
	if err != nil {
		t.Fatal(err)
	}
	account := referencePrefix + "abcdefabcdefabcdefabcdefabcdefab"
	_ = first.Delete(context.Background(), account)
	t.Cleanup(func() { _ = first.Delete(context.Background(), account) })
	start := make(chan struct{})
	results := make(chan error, 2)
	var waitGroup sync.WaitGroup
	for _, operation := range []struct {
		adapter vault
		secret  []byte
	}{{first, []byte("first")}, {second, []byte("second")}} {
		waitGroup.Add(1)
		go func(adapter vault, secret []byte) {
			defer waitGroup.Done()
			<-start
			results <- adapter.Create(context.Background(), account, secret)
		}(operation.adapter, operation.secret)
	}
	close(start)
	waitGroup.Wait()
	close(results)
	var successes, duplicates int
	for result := range results {
		switch {
		case result == nil:
			successes++
		case errors.Is(result, ErrDuplicate):
			duplicates++
		default:
			t.Fatalf("concurrent Create error = %v", result)
		}
	}
	if successes != 1 || duplicates != 1 {
		t.Fatalf("concurrent Create results: success=%d duplicate=%d", successes, duplicates)
	}
}
