package pairingv2

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	PairingService = "_s3rlcd-pair._tcp"
	PairingDomain  = "local"

	defaultCandidateTTL  = 10 * time.Second
	candidateRefBytes    = 16
	windowIDBytes        = 16
	maxReferenceAttempts = 8
)

var (
	ErrCandidateNotFound = errors.New("pairing candidate not found")
	ErrCandidateExpired  = errors.New("pairing candidate expired")
	ErrNoUsableInterface = errors.New("no usable local network interface")
)

type Clock interface {
	Now() time.Time
}

type Source interface {
	Browse(context.Context) ([]Observation, error)
}

// Candidate is the complete browser-safe discovery projection. In particular,
// it cannot represent an address, interface, DNS-SD instance, or window ID.
type Candidate struct {
	Reference string    `json:"candidate_ref"`
	Label     string    `json:"label"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Observation is the untrusted, backend-only result of one interface-scoped
// DNS-SD query. Discovery validates every field before retaining it.
type Observation struct {
	Instance       string
	InterfaceIndex int
	InterfaceName  string
	Address        netip.Addr
	Port           uint16
	TXT            []string
}

type Route struct {
	InterfaceIndex int
	InterfaceName  string
	Address        netip.Addr
	Port           uint16
}

// Selection is the backend-only route to an untrusted Pairing Window. It is
// deliberately unavailable to the management browser.
type Selection struct {
	WindowID [windowIDBytes]byte
	Routes   []Route
}

type DiscoveryConfig struct {
	Source       Source
	Clock        Clock
	Random       io.Reader
	CandidateTTL time.Duration
}

type Discovery struct {
	source Source
	clock  Clock
	random io.Reader
	ttl    time.Duration

	scanMutex sync.Mutex
	mutex     sync.Mutex
	entries   map[string]catalogEntry
}

type catalogEntry struct {
	expiresAt time.Time
	windowID  [windowIDBytes]byte
	routes    []Route
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

func NewDiscovery(config DiscoveryConfig) (*Discovery, error) {
	if config.Source == nil {
		return nil, errors.New("pairing discovery source is required")
	}
	if config.Clock == nil {
		config.Clock = systemClock{}
	}
	if config.Random == nil {
		config.Random = rand.Reader
	}
	if config.CandidateTTL == 0 {
		config.CandidateTTL = defaultCandidateTTL
	}
	if config.CandidateTTL < time.Second || config.CandidateTTL > 30*time.Second {
		return nil, errors.New("pairing candidate TTL must be between one and thirty seconds")
	}
	return &Discovery{
		source:  config.Source,
		clock:   config.Clock,
		random:  config.Random,
		ttl:     config.CandidateTTL,
		entries: make(map[string]catalogEntry),
	}, nil
}

func (discovery *Discovery) Scan(ctx context.Context) ([]Candidate, error) {
	discovery.scanMutex.Lock()
	defer discovery.scanMutex.Unlock()

	// A new scan is an authority boundary. Never allow a caller to resolve a
	// reference from an earlier or failed scan while network state is changing.
	discovery.replaceCatalog(nil)

	observations, err := discovery.source.Browse(ctx)
	if err != nil {
		return nil, fmt.Errorf("browse Pairing v2 candidates: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	groups := make(map[[windowIDBytes]byte][]Route)
	for _, observation := range observations {
		windowID, valid := validateObservation(observation)
		if !valid {
			continue
		}
		route := Route{
			InterfaceIndex: observation.InterfaceIndex,
			InterfaceName:  observation.InterfaceName,
			Address:        observation.Address,
			Port:           observation.Port,
		}
		groups[windowID] = appendUniqueRoute(groups[windowID], route)
	}

	windowIDs := make([][windowIDBytes]byte, 0, len(groups))
	for windowID := range groups {
		windowIDs = append(windowIDs, windowID)
	}
	sort.Slice(windowIDs, func(left, right int) bool {
		return strings.Compare(hex.EncodeToString(windowIDs[left][:]), hex.EncodeToString(windowIDs[right][:])) < 0
	})

	now := discovery.clock.Now().UTC()
	expiresAt := now.Add(discovery.ttl)
	entries := make(map[string]catalogEntry, len(windowIDs))
	candidates := make([]Candidate, 0, len(windowIDs))
	for _, windowID := range windowIDs {
		reference, err := discovery.uniqueReference(entries)
		if err != nil {
			discovery.replaceCatalog(nil)
			return nil, err
		}
		routes := append([]Route(nil), groups[windowID]...)
		sort.Slice(routes, func(left, right int) bool {
			if routes[left].InterfaceIndex != routes[right].InterfaceIndex {
				return routes[left].InterfaceIndex < routes[right].InterfaceIndex
			}
			if routes[left].Address != routes[right].Address {
				return routes[left].Address.Less(routes[right].Address)
			}
			return routes[left].Port < routes[right].Port
		})
		entries[reference] = catalogEntry{expiresAt: expiresAt, windowID: windowID, routes: routes}
		candidates = append(candidates, Candidate{
			Reference: reference,
			Label:     "S3 RLCD Deck · " + strings.ToUpper(hex.EncodeToString(windowID[:2])),
			ExpiresAt: expiresAt,
		})
	}

	discovery.replaceCatalog(entries)
	return candidates, nil
}

func (discovery *Discovery) Resolve(reference string) (Selection, error) {
	discovery.mutex.Lock()
	defer discovery.mutex.Unlock()

	entry, exists := discovery.entries[reference]
	if !exists {
		return Selection{}, ErrCandidateNotFound
	}
	if !discovery.clock.Now().UTC().Before(entry.expiresAt) {
		delete(discovery.entries, reference)
		return Selection{}, ErrCandidateExpired
	}
	return Selection{WindowID: entry.windowID, Routes: append([]Route(nil), entry.routes...)}, nil
}

func (discovery *Discovery) replaceCatalog(entries map[string]catalogEntry) {
	if entries == nil {
		entries = make(map[string]catalogEntry)
	}
	discovery.mutex.Lock()
	discovery.entries = entries
	discovery.mutex.Unlock()
}

func (discovery *Discovery) uniqueReference(entries map[string]catalogEntry) (string, error) {
	buffer := make([]byte, candidateRefBytes)
	for attempt := 0; attempt < maxReferenceAttempts; attempt++ {
		if _, err := io.ReadFull(discovery.random, buffer); err != nil {
			return "", fmt.Errorf("generate opaque pairing candidate reference: %w", err)
		}
		reference := base64.RawURLEncoding.EncodeToString(buffer)
		if _, exists := entries[reference]; !exists {
			return reference, nil
		}
	}
	return "", errors.New("generate unique opaque pairing candidate reference")
}

func validateObservation(observation Observation) ([windowIDBytes]byte, bool) {
	var empty [windowIDBytes]byte
	if observation.Instance == "" || len(observation.Instance) > 253 || strings.ContainsAny(observation.Instance, "\r\n\x00") {
		return empty, false
	}
	if observation.InterfaceIndex <= 0 || observation.InterfaceName == "" ||
		strings.ContainsAny(observation.InterfaceName, "\r\n\x00") {
		return empty, false
	}
	if !usablePairingIPv4(observation.Address) || observation.Port == 0 {
		return empty, false
	}
	values, valid := strictTXT(observation.TXT)
	if !valid || values["pv"] != "2" || values["model"] != "s3-rlcd-deck" || values["pairable"] != "1" {
		return empty, false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(values["iid"])
	if err != nil || len(decoded) != windowIDBytes || len(values["iid"]) != 22 {
		return empty, false
	}
	var windowID [windowIDBytes]byte
	copy(windowID[:], decoded)
	return windowID, true
}

func strictTXT(fields []string) (map[string]string, bool) {
	if len(fields) != 4 {
		return nil, false
	}
	allowed := map[string]struct{}{"pv": {}, "model": {}, "pairable": {}, "iid": {}}
	values := make(map[string]string, len(fields))
	for _, field := range fields {
		if len(field) == 0 || len(field) > 255 || strings.ContainsAny(field, "\r\n\x00") {
			return nil, false
		}
		key, value, found := strings.Cut(field, "=")
		if !found || key == "" || value == "" {
			return nil, false
		}
		if _, exists := allowed[key]; !exists {
			return nil, false
		}
		if _, duplicate := values[key]; duplicate {
			return nil, false
		}
		values[key] = value
	}
	return values, len(values) == len(allowed)
}

func appendUniqueRoute(routes []Route, candidate Route) []Route {
	for _, route := range routes {
		if route == candidate {
			return routes
		}
	}
	return append(routes, candidate)
}

func usablePairingIPv4(address netip.Addr) bool {
	address = address.Unmap()
	return address.Is4() && (address.IsPrivate() || netip.MustParsePrefix("100.64.0.0/10").Contains(address))
}
