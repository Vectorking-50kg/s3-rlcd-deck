package structuredprovider

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
)

type Collector struct {
	config    normalizedConfig
	client    httpClient
	transport *http.Transport

	mutex         sync.Mutex
	started       bool
	lastProvider  *aisnapshot.Provider
	requestPermit chan struct{}
}

func New(config Config) (*Collector, error) {
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	client := normalized.client
	var transport *http.Transport
	if client == nil {
		var safeClient *http.Client
		safeClient, transport = safeHTTPClient(normalized)
		client = safeClient
	}
	return &Collector{
		config: normalized, client: client, transport: transport,
		requestPermit: make(chan struct{}, 1),
	}, nil
}

func (collector *Collector) ProviderID() string {
	return collector.config.Definition.ID
}

// Run periodically publishes normalized Provider state. Request, parse, and
// credential failures remain scoped to this Provider and retain the last valid
// page as degraded state.
func (collector *Collector) Run(ctx context.Context, publish Publisher) error {
	if publish == nil {
		return ErrInvalidConfig
	}
	collector.mutex.Lock()
	if collector.started {
		collector.mutex.Unlock()
		return ErrAlreadyRunning
	}
	collector.started = true
	collector.mutex.Unlock()
	defer func() {
		if collector.transport != nil {
			collector.transport.CloseIdleConnections()
		}
		collector.mutex.Lock()
		collector.started = false
		collector.mutex.Unlock()
	}()

	for {
		provider, err := collector.collectRetained(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		if err = publish(ctx, provider); err != nil {
			return err
		}
		timer := time.NewTimer(collector.config.refreshInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}

// TestRequest performs one normal collection through the same safe transport
// and returns only a redacted normalized preview plus fixed diagnostics.
func (collector *Collector) TestRequest(ctx context.Context) (Preview, error) {
	requestContext, cancel := context.WithTimeout(ctx, collector.config.requestTimeout)
	result, err := collector.performRequest(requestContext)
	cancel()
	collector.publishDiagnostic(result.diagnostic)
	preview := Preview{
		Provider:   result.provider.Clone(),
		Diagnostic: result.diagnostic,
		Warning:    collector.config.warning,
	}
	return preview, err
}

func (collector *Collector) collectRetained(ctx context.Context) (aisnapshot.Provider, error) {
	requestContext, cancel := context.WithTimeout(ctx, collector.config.requestTimeout)
	result, err := collector.performRequest(requestContext)
	cancel()
	collector.publishDiagnostic(result.diagnostic)
	if ctx.Err() != nil {
		return aisnapshot.Provider{}, ctx.Err()
	}
	collector.mutex.Lock()
	defer collector.mutex.Unlock()
	if err == nil {
		owned := result.provider.Clone()
		collector.lastProvider = &owned
		return owned.Clone(), nil
	}
	problem := providerError(err)
	if collector.lastProvider == nil {
		return unavailableProvider(collector.config.Definition, problem), nil
	}
	retained := collector.lastProvider.Clone()
	retained.Status = aisnapshot.ProviderDegraded
	retained.Error = problem
	return retained, nil
}

func (collector *Collector) performRequest(ctx context.Context) (collectionResult, error) {
	select {
	case collector.requestPermit <- struct{}{}:
		defer func() { <-collector.requestPermit }()
	case <-ctx.Done():
		return collectionResult{
			diagnostic: Diagnostic{
				ProviderID:     collector.config.Definition.ID,
				AdapterVersion: AdapterVersion,
				ErrorCode:      "timeout",
			},
		}, ctx.Err()
	}
	return collectOnce(ctx, collector.config, collector.client)
}

func (collector *Collector) publishDiagnostic(diagnostic Diagnostic) {
	if collector.config.Diagnostic != nil {
		collector.config.Diagnostic(diagnostic)
	}
}

func unavailableProvider(
	definition Definition,
	problem *aisnapshot.ProviderError,
) aisnapshot.Provider {
	return aisnapshot.Provider{
		SchemaVersion: aisnapshot.SchemaVersion{Major: aisnapshot.SchemaMajor, Minor: aisnapshot.SchemaMinor},
		ID:            definition.ID,
		DisplayName:   definition.DisplayName,
		Status:        aisnapshot.ProviderUnavailable,
		Source:        aisnapshot.ProviderSourceNone,
		Confidence:    aisnapshot.ConfidenceUnavailable,
		Experimental:  definition.Experimental,
		Windows:       []aisnapshot.QuotaWindow{},
		Error:         problem,
	}
}

func providerError(err error) *aisnapshot.ProviderError {
	problem := &aisnapshot.ProviderError{Retryable: true}
	switch {
	case errors.Is(err, ErrAuthStale):
		problem.Code = aisnapshot.ProviderErrorAuthStale
		problem.Retryable = false
	case errors.Is(err, ErrPermission), errors.Is(err, ErrNetworkPolicy):
		problem.Code = aisnapshot.ProviderErrorPermissionDenied
		problem.Retryable = false
	case errors.Is(err, context.DeadlineExceeded):
		problem.Code = aisnapshot.ProviderErrorTimeout
	case errors.Is(err, ErrSchemaChanged), errors.Is(err, ErrResponseTooLarge):
		problem.Code = aisnapshot.ProviderErrorSchemaChanged
		problem.Retryable = false
	default:
		problem.Code = aisnapshot.ProviderErrorUnavailable
	}
	return problem
}
