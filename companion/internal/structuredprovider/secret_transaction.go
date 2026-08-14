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
// credentials to a persistable Definition. PutNew invokes its callback only
// after reserving a collision-free reference and before writing secret bytes.
// The production *secretstore.Store satisfies it directly.
type DefinitionSecretStore interface {
	PutNew(
		context.Context,
		[]byte,
		func(secretstore.Reference) error,
	) (secretstore.Reference, error)
	Delete(context.Context, secretstore.Reference) error
}

// DefinitionOwner is the durable configuration side of the cross-store
// transaction. StageCleanup persists newly-created references before they can
// be published. Publish atomically replaces the non-secret definition, removes
// activated references from the cleanup journal, and journals references
// retired by the replacement. CompleteCleanup removes idempotently-deleted
// references from that journal.
type DefinitionOwner interface {
	StageCleanup(context.Context, []secretstore.Reference) error
	Publish(
		context.Context,
		*Definition,
		Definition,
		[]secretstore.Reference,
	) (bool, error)
	CompleteCleanup(context.Context, []secretstore.Reference) error
}

// SecretRollbackError keeps failed-compensation references observable without
// exposing credential bytes. Pending references have already been persisted by
// DefinitionOwner and are retried on the next startup.
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
	current *Definition,
	definition Definition,
	bindings []SecretBinding,
	secrets DefinitionSecretStore,
	owner DefinitionOwner,
) (Definition, error) {
	if ctx == nil || secrets == nil || owner == nil {
		return Definition{}, ErrInvalidConfig
	}
	candidate := cloneDefinition(definition)
	seen := make(map[int]struct{}, len(bindings))
	created := make([]secretstore.Reference, 0, len(bindings))
	staged := make([]secretstore.Reference, 0, len(bindings))
	rollback := func(references []secretstore.Reference) []secretstore.Reference {
		rollbackContext, cancel := context.WithTimeout(context.Background(), secretRollbackTimeout)
		defer cancel()
		var pending []secretstore.Reference
		var deleted []secretstore.Reference
		for index := len(references) - 1; index >= 0; index-- {
			if err := secrets.Delete(rollbackContext, references[index]); err != nil {
				pending = append(pending, references[index])
			} else {
				deleted = append(deleted, references[index])
			}
		}
		if len(deleted) != 0 && owner.CompleteCleanup(rollbackContext, deleted) != nil {
			// A stale journal entry is safe: retry cleanup is idempotent. Keep it
			// observable so the caller does not mistake cleanup for complete.
			pending = append(pending, deleted...)
		}
		return pending
	}
	fail := func(reason error) (Definition, error) {
		if pending := rollback(staged); len(pending) != 0 {
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
		stageFailed := false
		reference, err := secrets.PutNew(ctx, binding.Value, func(reference secretstore.Reference) error {
			if stageErr := owner.StageCleanup(ctx, []secretstore.Reference{reference}); stageErr != nil {
				stageFailed = !errors.Is(stageErr, secretstore.ErrDuplicate)
				return stageErr
			}
			staged = append(staged, reference)
			return nil
		})
		if err != nil {
			if stageFailed {
				return fail(ErrDefinitionCommit)
			}
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
	committed, publishErr := owner.Publish(ctx, current, cloneDefinition(persistable), created)
	if !committed {
		return fail(ErrDefinitionCommit)
	}
	if publishErr != nil {
		// The protected-file adapter can report a post-commit durability or
		// permission verification error. The new definition is authoritative;
		// never roll back references that it now owns.
		return persistable, ErrDefinitionCommit
	}
	retired := retiredReferences(current, persistable)
	cleanup := append(retired, unactivatedReferences(staged, created)...)
	if pending := rollback(cleanup); len(pending) != 0 {
		return persistable, &SecretRollbackError{pending: pending}
	}
	return persistable, nil
}

func unactivatedReferences(
	staged []secretstore.Reference,
	activated []secretstore.Reference,
) []secretstore.Reference {
	active := make(map[secretstore.Reference]struct{}, len(activated))
	for _, reference := range activated {
		active[reference] = struct{}{}
	}
	var pending []secretstore.Reference
	for _, reference := range staged {
		if _, used := active[reference]; !used {
			pending = append(pending, reference)
		}
	}
	return pending
}

// CommitCurlImport transfers every parsed header credential into the Secret
// Store, clears the import's caller-visible secret buffers, and atomically
// commits a Definition whose Request contains only opaque references.
func CommitCurlImport(
	ctx context.Context,
	current *Definition,
	definition Definition,
	imported *CurlImport,
	secrets DefinitionSecretStore,
	owner DefinitionOwner,
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
	committed, err := CommitDefinition(ctx, current, definition, bindings, secrets, owner)
	for index := range imported.Secrets {
		overwrite(imported.Secrets[index].Value)
		imported.Secrets[index].Value = nil
	}
	imported.Secrets = nil
	return committed, err
}

func retiredReferences(current *Definition, replacement Definition) []secretstore.Reference {
	if current == nil {
		return nil
	}
	retained := make(map[secretstore.Reference]struct{}, len(replacement.Request.Headers))
	for _, header := range replacement.Request.Headers {
		if header.SecretReference != "" {
			retained[header.SecretReference] = struct{}{}
		}
	}
	seen := make(map[secretstore.Reference]struct{}, len(current.Request.Headers))
	var retired []secretstore.Reference
	for _, header := range current.Request.Headers {
		reference := header.SecretReference
		if reference == "" {
			continue
		}
		if _, keep := retained[reference]; keep {
			continue
		}
		if _, duplicate := seen[reference]; duplicate {
			continue
		}
		seen[reference] = struct{}{}
		retired = append(retired, reference)
	}
	return retired
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
