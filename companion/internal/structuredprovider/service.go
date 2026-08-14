package structuredprovider

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/secretstore"
)

// HeaderView is the management-safe projection of one credential-backed
// header. The opaque Vault reference and credential bytes never cross this
// boundary.
type HeaderView struct {
	Name             string `json:"name"`
	Prefix           string `json:"prefix,omitempty"`
	SecretConfigured bool   `json:"secret_configured"`
}

type RequestView struct {
	Method  Method          `json:"method"`
	URL     string          `json:"url"`
	Headers []HeaderView    `json:"headers"`
	Body    json.RawMessage `json:"body,omitempty"`
}

type DefinitionView struct {
	ID                    string      `json:"id"`
	DisplayName           string      `json:"display_name"`
	Experimental          bool        `json:"experimental"`
	Request               RequestView `json:"request"`
	Mapping               Mapping     `json:"mapping"`
	RefreshMinutes        uint16      `json:"refresh_minutes"`
	RequestTimeoutSeconds uint16      `json:"request_timeout_seconds"`
	MaximumResponseBytes  int64       `json:"maximum_response_bytes"`
}

type serviceSecretStore interface {
	DefinitionSecretStore
	Get(context.Context, secretstore.Reference) ([]byte, error)
}

// Service is the single configuration/lifecycle seam used by the management
// API and the collector supervisor. Mutations publish one protected
// configuration first, then wake the supervisor without exposing credentials.
type Service struct {
	owner        *DefinitionStore
	secrets      serviceSecretStore
	changed      chan struct{}
	newCollector func(Config) (managedCollector, error)

	mutex   sync.Mutex
	running bool
}

type managedCollector interface {
	Run(context.Context, Publisher) error
	TestRequest(context.Context) (Preview, error)
}

// StatePublisher receives a complete, ordered, independently-owned view of
// the configured structured Providers. A Provider failure changes only its
// page to UNAVAILABLE; it never terminates sibling collectors or the service.
type StatePublisher func(context.Context, []string, []aisnapshot.Provider) error

type serviceCollectorEvent struct {
	id         string
	generation uint64
	provider   *aisnapshot.Provider
	err        error
}

type serviceCollectorRun struct {
	generation uint64
	definition Definition
	cancel     context.CancelFunc
}

func NewService(owner *DefinitionStore, secrets serviceSecretStore) (*Service, error) {
	if owner == nil || secrets == nil {
		return nil, ErrInvalidConfig
	}
	if _, err := owner.Configuration(context.Background()); err != nil {
		return nil, ErrDefinitionCommit
	}
	return &Service{
		owner: owner, secrets: secrets, changed: make(chan struct{}, 1),
		newCollector: func(config Config) (managedCollector, error) { return New(config) },
	}, nil
}

func (service *Service) List(ctx context.Context) ([]DefinitionView, error) {
	if service == nil || ctx == nil {
		return nil, ErrInvalidConfig
	}
	definitions, err := service.owner.Definitions(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]DefinitionView, len(definitions))
	for index := range definitions {
		views[index] = definitionView(definitions[index])
	}
	return views, nil
}

func (service *Service) Templates() []DefinitionView {
	templates := Templates()
	views := make([]DefinitionView, len(templates))
	for index := range templates {
		views[index] = definitionView(templates[index].Definition)
	}
	return views
}

func (service *Service) Save(
	ctx context.Context,
	currentID string,
	definition Definition,
	bindings []SecretBinding,
	keepExisting []int,
) (DefinitionView, error) {
	for index := range bindings {
		defer overwrite(bindings[index].Value)
		defer func(index int) { bindings[index].Value = nil }(index)
	}
	if service == nil || ctx == nil {
		return DefinitionView{}, ErrInvalidConfig
	}
	for _, header := range definition.Request.Headers {
		if header.SecretReference != "" {
			return DefinitionView{}, ErrInvalidConfig
		}
	}
	definitions, err := service.owner.Definitions(ctx)
	if err != nil {
		return DefinitionView{}, err
	}
	var current *Definition
	if currentID != "" {
		for index := range definitions {
			if definitions[index].ID == currentID {
				owned := cloneDefinition(definitions[index])
				current = &owned
				break
			}
		}
		if current == nil || definition.ID != currentID {
			return DefinitionView{}, ErrInvalidConfig
		}
	}
	covered := make(map[int]struct{}, len(bindings)+len(keepExisting))
	for _, binding := range bindings {
		if binding.HeaderIndex < 0 || binding.HeaderIndex >= len(definition.Request.Headers) {
			return DefinitionView{}, ErrInvalidConfig
		}
		if _, duplicate := covered[binding.HeaderIndex]; duplicate {
			return DefinitionView{}, ErrInvalidConfig
		}
		covered[binding.HeaderIndex] = struct{}{}
	}
	for _, index := range keepExisting {
		if current == nil || index < 0 || index >= len(definition.Request.Headers) ||
			index >= len(current.Request.Headers) {
			return DefinitionView{}, ErrInvalidConfig
		}
		if _, duplicate := covered[index]; duplicate {
			return DefinitionView{}, ErrInvalidConfig
		}
		if definition.Request.Headers[index].Name != current.Request.Headers[index].Name ||
			definition.Request.Headers[index].Prefix != current.Request.Headers[index].Prefix {
			return DefinitionView{}, ErrInvalidConfig
		}
		definition.Request.Headers[index].SecretReference =
			current.Request.Headers[index].SecretReference
		covered[index] = struct{}{}
	}
	if len(covered) != len(definition.Request.Headers) {
		return DefinitionView{}, ErrInvalidConfig
	}
	committed, err := CommitDefinition(
		ctx,
		current,
		definition,
		bindings,
		service.secrets,
		service.owner,
	)
	if committed.ID != "" {
		service.notifyChanged()
	}
	if err != nil {
		return definitionView(committed), err
	}
	return definitionView(committed), nil
}

func (service *Service) Reorder(ctx context.Context, providerIDs []string) error {
	if service == nil || ctx == nil {
		return ErrInvalidConfig
	}
	err := service.owner.ReorderDefinitions(ctx, providerIDs)
	if err == nil {
		service.notifyChanged()
	}
	return err
}

func (service *Service) Delete(ctx context.Context, providerID string) error {
	if service == nil || ctx == nil {
		return ErrInvalidConfig
	}
	err := service.owner.DeleteDefinition(ctx, providerID, service.secrets)
	service.notifyChanged()
	return err
}

// Test performs one collection with the persisted definition and secret
// references. Its result is already normalized and contains no raw request,
// response, URL, credential, or account payload.
func (service *Service) Test(ctx context.Context, providerID string) (Preview, error) {
	if service == nil || ctx == nil {
		return Preview{}, ErrInvalidConfig
	}
	definitions, err := service.owner.Definitions(ctx)
	if err != nil {
		return Preview{}, err
	}
	for _, definition := range definitions {
		if definition.ID != providerID {
			continue
		}
		collector, createErr := service.newCollector(Config{Definition: definition, Secrets: service.secrets})
		if createErr != nil {
			return Preview{}, createErr
		}
		return collector.TestRequest(ctx)
	}
	return Preview{}, ErrInvalidConfig
}

// Run reconciles persisted definitions into independently supervised
// collectors. It is intentionally the only dynamic collector lifecycle seam:
// management mutations merely commit configuration and wake this loop.
func (service *Service) Run(ctx context.Context, publish StatePublisher) error {
	if service == nil || ctx == nil || publish == nil {
		return ErrInvalidConfig
	}
	service.mutex.Lock()
	if service.running {
		service.mutex.Unlock()
		return ErrAlreadyRunning
	}
	service.running = true
	service.mutex.Unlock()
	defer func() {
		service.mutex.Lock()
		service.running = false
		service.mutex.Unlock()
	}()

	events := make(chan serviceCollectorEvent, maximumStoredDefinitions*2)
	runs := make(map[string]serviceCollectorRun)
	states := make(map[string]aisnapshot.Provider)
	var order []string
	var generation uint64
	var collectors sync.WaitGroup

	start := func(parent context.Context, definition Definition) error {
		collector, err := service.newCollector(Config{Definition: definition, Secrets: service.secrets})
		if err != nil {
			return err
		}
		generation++
		ownedGeneration := generation
		collectorContext, cancel := context.WithCancel(parent)
		runs[definition.ID] = serviceCollectorRun{
			generation: ownedGeneration,
			definition: cloneDefinition(definition),
			cancel:     cancel,
		}
		collectors.Add(1)
		go func(id string) {
			defer collectors.Done()
			runErr := collector.Run(collectorContext, func(
				publishContext context.Context,
				provider aisnapshot.Provider,
			) error {
				owned := provider.Clone()
				select {
				case events <- serviceCollectorEvent{
					id: id, generation: ownedGeneration, provider: &owned,
				}:
					return nil
				case <-publishContext.Done():
					return publishContext.Err()
				}
			})
			events <- serviceCollectorEvent{id: id, generation: ownedGeneration, err: runErr}
		}(definition.ID)
		return nil
	}
	stopAll := func() error {
		for _, run := range runs {
			run.cancel()
		}
		allStopped := make(chan struct{})
		go func() {
			collectors.Wait()
			close(allStopped)
		}()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case <-events:
				// Drain both current and superseded generations until every
				// collector has released its request and transport ownership.
			case <-allStopped:
				return nil
			case <-deadline.C:
				return context.DeadlineExceeded
			}
		}
	}

	publishState := func() error {
		providers := make([]aisnapshot.Provider, 0, len(order))
		for _, id := range order {
			if provider, exists := states[id]; exists {
				providers = append(providers, provider.Clone())
			}
		}
		return publish(ctx, append([]string(nil), order...), providers)
	}

	reconcile := func() error {
		definitions, err := service.owner.Definitions(ctx)
		if err != nil {
			return err
		}
		next := make(map[string]Definition, len(definitions))
		nextOrder := make([]string, len(definitions))
		for index, definition := range definitions {
			next[definition.ID] = definition
			nextOrder[index] = definition.ID
		}
		for id, run := range runs {
			definition, exists := next[id]
			if exists && reflect.DeepEqual(definition, run.definition) {
				continue
			}
			run.cancel()
			delete(runs, id)
			delete(states, id)
		}
		for _, definition := range definitions {
			if _, exists := runs[definition.ID]; exists {
				continue
			}
			if err = start(ctx, definition); err != nil {
				states[definition.ID] = unavailableProvider(
					definition,
					&aisnapshot.ProviderError{
						Code: aisnapshot.ProviderErrorUnavailable, Retryable: true,
					},
				)
			}
		}
		order = nextOrder
		return publishState()
	}

	if err := reconcile(); err != nil {
		_ = stopAll()
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return stopAll()
		case <-service.changed:
			if err := reconcile(); err != nil {
				_ = stopAll()
				return err
			}
		case event := <-events:
			run, current := runs[event.id]
			if !current || run.generation != event.generation {
				continue
			}
			if event.provider != nil {
				states[event.id] = event.provider.Clone()
				if err := publishState(); err != nil {
					run.cancel()
				}
				continue
			}
			delete(runs, event.id)
			if ctx.Err() == nil && event.err != nil {
				states[event.id] = unavailableProvider(
					run.definition,
					&aisnapshot.ProviderError{
						Code: aisnapshot.ProviderErrorUnavailable, Retryable: true,
					},
				)
				_ = publishState()
			}
		}
	}
}

func (service *Service) notifyChanged() {
	select {
	case service.changed <- struct{}{}:
	default:
	}
}

func definitionView(definition Definition) DefinitionView {
	headers := make([]HeaderView, len(definition.Request.Headers))
	for index, header := range definition.Request.Headers {
		headers[index] = HeaderView{
			Name:             header.Name,
			Prefix:           header.Prefix,
			SecretConfigured: header.SecretReference != "",
		}
	}
	return DefinitionView{
		ID:           definition.ID,
		DisplayName:  definition.DisplayName,
		Experimental: definition.Experimental,
		Request: RequestView{
			Method: definition.Request.Method, URL: definition.Request.URL,
			Headers: headers, Body: append(json.RawMessage(nil), definition.Request.Body...),
		},
		Mapping:               definition.Mapping,
		RefreshMinutes:        definition.RefreshMinutes,
		RequestTimeoutSeconds: definition.RequestTimeoutSeconds,
		MaximumResponseBytes:  definition.MaximumResponseBytes,
	}
}
