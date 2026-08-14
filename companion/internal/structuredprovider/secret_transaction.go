package structuredprovider

import (
	"context"
	"errors"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/secretstore"
)

var (
	ErrDefinitionCommit = errors.New("structured Provider definition commit failed")
	ErrSecretRollback   = errors.New("structured Provider secret rollback failed")
)

const secretRollbackTimeout = 5 * time.Second

// SecretBinding transfers one caller-owned credential into the Secret Store
// position identified by HeaderIndex. Value is never placed in Definition.
type SecretBinding struct {
	HeaderIndex int
	Value       []byte
}

// DefinitionSecretStore is the exact mutation seam needed to bind new
// credentials to a persistable Definition. The production *secretstore.Store
// satisfies it directly.
type DefinitionSecretStore interface {
	Put(context.Context, secretstore.Reference, []byte) (secretstore.Reference, error)
	Delete(context.Context, secretstore.Reference) error
}

// DefinitionCommit must atomically replace the non-secret configuration or
// return an error without publishing a partial definition.
type DefinitionCommit func(context.Context, Definition) error

// SecretRollbackError keeps failed-compensation references observable without
// exposing credential bytes. A config owner must persist/retry these references
// until Delete succeeds; they are never silently orphaned.
type SecretRollbackError struct {
	pending []secretstore.Reference
}

func (failure *SecretRollbackError) Error() string { return ErrSecretRollback.Error() }

func (failure *SecretRollbackError) Is(target error) bool { return target == ErrSecretRollback }

func (failure *SecretRollbackError) PendingReferences() []secretstore.Reference {
	if failure == nil {
		return nil
	}
	return append([]secretstore.Reference(nil), failure.pending...)
}

// CommitDefinition creates all new credentials, replaces draft header slots
// with opaque Secret References, validates the persistable Definition, then
// commits it. A failed validation or config commit compensates every created
// credential before returning a fixed, non-secret error.
//
// Existing references are deliberately not replaced here: changing a value at
// an existing reference is the Secret Store's atomic Put(update) operation and
// requires no configuration rewrite.
func CommitDefinition(
	ctx context.Context,
	definition Definition,
	bindings []SecretBinding,
	secrets DefinitionSecretStore,
	commit DefinitionCommit,
) (Definition, error) {
	if ctx == nil || secrets == nil || commit == nil {
		return Definition{}, ErrInvalidConfig
	}
	candidate := cloneDefinition(definition)
	seen := make(map[int]struct{}, len(bindings))
	created := make([]secretstore.Reference, 0, len(bindings))
	rollback := func() []secretstore.Reference {
		rollbackContext, cancel := context.WithTimeout(context.Background(), secretRollbackTimeout)
		defer cancel()
		var pending []secretstore.Reference
		for index := len(created) - 1; index >= 0; index-- {
			if err := secrets.Delete(rollbackContext, created[index]); err != nil {
				pending = append(pending, created[index])
			}
		}
		return pending
	}
	fail := func(reason error) (Definition, error) {
		if pending := rollback(); len(pending) != 0 {
			return Definition{}, &SecretRollbackError{pending: pending}
		}
		return Definition{}, reason
	}
	for _, binding := range bindings {
		if binding.HeaderIndex < 0 || binding.HeaderIndex >= len(candidate.Request.Headers) ||
			len(binding.Value) == 0 {
			return fail(ErrInvalidConfig)
		}
		if _, duplicate := seen[binding.HeaderIndex]; duplicate {
			return fail(ErrInvalidConfig)
		}
		seen[binding.HeaderIndex] = struct{}{}
		header := &candidate.Request.Headers[binding.HeaderIndex]
		if header.SecretReference != "" {
			return fail(ErrInvalidConfig)
		}
		reference, err := secrets.Put(ctx, "", binding.Value)
		if err != nil {
			return fail(normalizeSecretStoreError(err))
		}
		created = append(created, reference)
		header.SecretReference = reference
	}
	normalized, err := normalizeConfig(Config{Definition: candidate})
	if err != nil {
		return fail(err)
	}
	persistable := cloneDefinition(normalized.Definition)
	if err = commit(ctx, cloneDefinition(persistable)); err != nil {
		return fail(ErrDefinitionCommit)
	}
	return persistable, nil
}

// CommitCurlImport transfers every parsed header credential into the Secret
// Store, clears the import's caller-visible secret buffers, and atomically
// commits a Definition whose Request contains only opaque references.
func CommitCurlImport(
	ctx context.Context,
	definition Definition,
	imported *CurlImport,
	secrets DefinitionSecretStore,
	commit DefinitionCommit,
) (Definition, error) {
	if imported == nil {
		return Definition{}, ErrInvalidCurl
	}
	bindings := make([]SecretBinding, len(imported.Secrets))
	for index := range imported.Secrets {
		bindings[index] = SecretBinding{
			HeaderIndex: imported.Secrets[index].HeaderIndex,
			Value:       imported.Secrets[index].Value,
		}
	}
	definition.Request = imported.Request
	committed, err := CommitDefinition(ctx, definition, bindings, secrets, commit)
	for index := range imported.Secrets {
		overwrite(imported.Secrets[index].Value)
		imported.Secrets[index].Value = nil
	}
	imported.Secrets = nil
	return committed, err
}

func cloneDefinition(source Definition) Definition {
	cloned := source
	cloned.Request.Headers = append([]Header(nil), source.Request.Headers...)
	cloned.Request.Body = append([]byte(nil), source.Request.Body...)
	return cloned
}

func normalizeSecretStoreError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	for _, known := range []error{
		secretstore.ErrLocked,
		secretstore.ErrPermission,
		secretstore.ErrCanceled,
		secretstore.ErrUnavailable,
		secretstore.ErrInvalid,
		secretstore.ErrCorrupt,
	} {
		if errors.Is(err, known) {
			return known
		}
	}
	return secretstore.ErrUnavailable
}
