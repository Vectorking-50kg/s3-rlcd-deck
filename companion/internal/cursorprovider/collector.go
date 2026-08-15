package cursorprovider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
)

type unavailableTokenSource struct{}

func (unavailableTokenSource) AccessToken(context.Context) ([]byte, error) {
	return nil, ErrUnavailable
}

type Collector struct {
	config Config

	mutex        sync.Mutex
	started      bool
	lastProvider *aisnapshot.Provider
}

func New(config Config) (*Collector, error) {
	if config.AdapterVersion == 0 {
		config.AdapterVersion = AdapterVersion
	}
	if config.ResponseSchemaVersion == 0 {
		config.ResponseSchemaVersion = ResponseSchemaVersion
	}
	if config.AdapterVersion != AdapterVersion {
		return nil, fmt.Errorf("unsupported Cursor adapter version %d", config.AdapterVersion)
	}
	if config.ResponseSchemaVersion != ResponseSchemaVersion {
		return nil, fmt.Errorf(
			"unsupported Cursor response schema version %d",
			config.ResponseSchemaVersion,
		)
	}
	if config.TokenSource == nil {
		source, err := NewSQLiteTokenSource("")
		if err != nil {
			config.TokenSource = unavailableTokenSource{}
		} else {
			config.TokenSource = source
		}
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = defaultRequestTimeout
	}
	if config.RefreshInterval <= 0 {
		config.RefreshInterval = defaultRefreshInterval
	}
	if config.RetryInterval <= 0 {
		config.RetryInterval = defaultRetryInterval
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.endpointURL == "" {
		config.endpointURL = privateUsageEndpointV1
	}
	if config.maximumResponse <= 0 {
		config.maximumResponse = defaultMaximumResponse
	}
	if config.maximumResponse > defaultMaximumResponse {
		return nil, fmt.Errorf("maximum Cursor response exceeds %d bytes", defaultMaximumResponse)
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Transport: http.DefaultTransport}
	}
	isolatedClient := *client
	isolatedClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	config.HTTPClient = &isolatedClient
	return &Collector{config: config}, nil
}

// Run periodically reads a fresh Cursor access token and requests the pinned
// private endpoint. Collection failures are converted to Cursor-only provider
// state and never escape into another Companion subsystem.
func (collector *Collector) Run(ctx context.Context, publish Publisher) error {
	if publish == nil {
		return errors.New("Cursor publisher is required")
	}
	collector.mutex.Lock()
	if collector.started {
		collector.mutex.Unlock()
		return ErrAlreadyRunning
	}
	collector.started = true
	collector.mutex.Unlock()
	defer func() {
		collector.mutex.Lock()
		collector.started = false
		collector.mutex.Unlock()
	}()

	for {
		provider, succeeded, err := collector.collectRetained(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		if err = publish(ctx, provider); err != nil {
			return err
		}
		interval := collector.config.RetryInterval
		if succeeded {
			interval = collector.config.RefreshInterval
		}
		timer := time.NewTimer(interval)
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

func (collector *Collector) collectRetained(
	ctx context.Context,
) (aisnapshot.Provider, bool, error) {
	requestContext, cancel := context.WithTimeout(ctx, collector.config.RequestTimeout)
	provider, err := collectProvider(requestContext, collector.config)
	cancel()
	if ctx.Err() != nil {
		return aisnapshot.Provider{}, false, ctx.Err()
	}
	collector.mutex.Lock()
	defer collector.mutex.Unlock()
	if err == nil {
		owned := provider.Clone()
		collector.lastProvider = &owned
		return owned.Clone(), true, nil
	}
	problem := providerError(err)
	if collector.lastProvider == nil {
		return unavailableProvider(problem), false, nil
	}
	retained := collector.lastProvider.Clone()
	retained.Status = aisnapshot.ProviderDegraded
	retained.Error = problem
	return retained, false, nil
}

func unavailableProvider(problem *aisnapshot.ProviderError) aisnapshot.Provider {
	return aisnapshot.Provider{
		SchemaVersion: aisnapshot.SchemaVersion{
			Major: aisnapshot.SchemaMajor,
			Minor: aisnapshot.SchemaMinor,
		},
		ID:           providerID,
		DisplayName:  providerName,
		Status:       aisnapshot.ProviderUnavailable,
		Source:       aisnapshot.ProviderSourceNone,
		Confidence:   aisnapshot.ConfidenceUnavailable,
		Experimental: true,
		Windows:      []aisnapshot.QuotaWindow{},
		Error:        problem,
	}
}

func providerError(err error) *aisnapshot.ProviderError {
	problem := &aisnapshot.ProviderError{Retryable: true}
	switch {
	case errors.Is(err, ErrNotLoggedIn):
		problem.Code = aisnapshot.ProviderErrorAuthStale
		problem.Retryable = false
	case errors.Is(err, ErrPermission):
		problem.Code = aisnapshot.ProviderErrorPermissionDenied
		problem.Retryable = false
	case errors.Is(err, context.DeadlineExceeded):
		problem.Code = aisnapshot.ProviderErrorTimeout
	case errors.Is(err, ErrSchemaChanged):
		problem.Code = aisnapshot.ProviderErrorSchemaChanged
		problem.Retryable = false
	default:
		problem.Code = aisnapshot.ProviderErrorUnavailable
	}
	return problem
}
