//go:build darwin

package secretstore

import (
	"errors"
	"testing"
)

func TestDarwinVaultStatusMappingIsStable(t *testing.T) {
	tests := []struct {
		status int32
		want   error
	}{
		{errSecSuccess, nil},
		{errSecDuplicateItem, ErrDuplicate},
		{errSecItemNotFound, ErrNotFound},
		{errSecInteractionNotAllowed, ErrLocked},
		{errSecUserCanceled, ErrCanceled},
		{errSecReadOnly, ErrPermission},
		{errSecAuthFailed, ErrPermission},
		{errSecMissingEntitlement, ErrPermission},
		{errSecParam, ErrInvalid},
		{errSecDecode, ErrCorrupt},
		{errSecNotAvailable, ErrUnavailable},
		{-999_999, ErrUnavailable},
	}
	for _, testCase := range tests {
		err := darwinVaultError(testCase.status)
		if testCase.want == nil && err != nil ||
			testCase.want != nil && !errors.Is(err, testCase.want) {
			t.Fatalf("darwinVaultError(%d) = %v, want %v", testCase.status, err, testCase.want)
		}
	}
}
