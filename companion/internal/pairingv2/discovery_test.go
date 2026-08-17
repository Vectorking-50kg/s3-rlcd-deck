package pairingv2

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mutex sync.Mutex
	now   time.Time
}

func (clock *fakeClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.now
}

func (clock *fakeClock) Advance(duration time.Duration) {
	clock.mutex.Lock()
	clock.now = clock.now.Add(duration)
	clock.mutex.Unlock()
}

type fakeSource struct {
	mutex        sync.Mutex
	observations []Observation
	err          error
}

func (source *fakeSource) Browse(context.Context) ([]Observation, error) {
	source.mutex.Lock()
	defer source.mutex.Unlock()
	return append([]Observation(nil), source.observations...), source.err
}

func validObservation(seed byte) Observation {
	windowID := bytes.Repeat([]byte{seed}, windowIDBytes)
	return Observation{
		Instance:       "window._s3rlcd-pair._tcp.local.",
		InterfaceIndex: 7,
		InterfaceName:  "en0",
		Address:        netip.MustParseAddr("192.168.50.23"),
		Port:           3232,
		TXT: []string{
			"model=s3-rlcd-deck",
			"iid=" + base64.RawURLEncoding.EncodeToString(windowID),
			"pairable=1",
			"pv=2",
		},
	}
}

func TestValidateObservationAcceptsOnlyTheMinimalPairingRecord(t *testing.T) {
	observation := validObservation(0x2a)
	windowID, valid := validateObservation(observation)
	if !valid || !bytes.Equal(windowID[:], bytes.Repeat([]byte{0x2a}, windowIDBytes)) {
		t.Fatalf("valid observation was rejected: valid=%v window=%x", valid, windowID)
	}

	tests := map[string]func(*Observation){
		"missing field":       func(value *Observation) { value.TXT = value.TXT[:3] },
		"extra field":         func(value *Observation) { value.TXT = append(value.TXT, "serial=secret") },
		"duplicate field":     func(value *Observation) { value.TXT[0] = "pv=2" },
		"unsupported version": func(value *Observation) { value.TXT[3] = "pv=3" },
		"wrong model":         func(value *Observation) { value.TXT[0] = "model=other" },
		"not pairable":        func(value *Observation) { value.TXT[2] = "pairable=0" },
		"padded iid":          func(value *Observation) { value.TXT[1] += "==" },
		"short iid":           func(value *Observation) { value.TXT[1] = "iid=AA" },
		"public address":      func(value *Observation) { value.Address = netip.MustParseAddr("203.0.113.9") },
		"zero port":           func(value *Observation) { value.Port = 0 },
		"missing interface":   func(value *Observation) { value.InterfaceIndex = 0 },
		"control in instance": func(value *Observation) { value.Instance = "deck\nspoof" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := validObservation(0x2a)
			mutate(&candidate)
			if _, valid := validateObservation(candidate); valid {
				t.Fatal("malformed observation was accepted")
			}
		})
	}
}

func TestDiscoveryExposesOnlyOpaqueShortLivedCandidates(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 18, 9, 30, 0, 0, time.UTC)}
	first := validObservation(0x11)
	duplicate := first
	secondRoute := first
	secondRoute.InterfaceIndex = 8
	secondRoute.InterfaceName = "en1"
	secondRoute.Address = netip.MustParseAddr("10.0.0.23")
	source := &fakeSource{observations: []Observation{first, duplicate, secondRoute}}
	randomBytes := bytes.Repeat([]byte{0xa5}, candidateRefBytes)
	discovery, err := NewDiscovery(DiscoveryConfig{
		Source: source, Clock: clock, Random: bytes.NewReader(randomBytes), CandidateTTL: 8 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	candidates, err := discovery.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidate count = %d, want 1", len(candidates))
	}
	candidate := candidates[0]
	if len(candidate.Reference) != 22 || strings.Contains(candidate.Reference, "192.168") {
		t.Fatalf("candidate reference is not an opaque 128-bit value: %q", candidate.Reference)
	}
	encoded, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"192.168.50.23", "10.0.0.23", "en0", "en1", "window._s3rlcd", base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x11}, windowIDBytes))} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("browser candidate leaked %q: %s", forbidden, encoded)
		}
	}
	if !candidate.ExpiresAt.Equal(clock.Now().Add(8 * time.Second)) {
		t.Fatalf("expires_at = %s", candidate.ExpiresAt)
	}

	selection, err := discovery.Resolve(candidate.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Routes) != 2 {
		t.Fatalf("route count = %d, want 2", len(selection.Routes))
	}
	selection.Routes[0].InterfaceName = "mutated"
	again, err := discovery.Resolve(candidate.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if again.Routes[0].InterfaceName == "mutated" {
		t.Fatal("Resolve returned catalog-owned mutable storage")
	}

	clock.Advance(8 * time.Second)
	if _, err := discovery.Resolve(candidate.Reference); !errors.Is(err, ErrCandidateExpired) {
		t.Fatalf("expired candidate error = %v", err)
	}
}

func TestDiscoveryInvalidatesEarlierReferencesBeforeARescan(t *testing.T) {
	clock := &fakeClock{now: time.Now().UTC()}
	source := &fakeSource{observations: []Observation{validObservation(1)}}
	discovery, err := NewDiscovery(DiscoveryConfig{
		Source: source, Clock: clock, Random: bytes.NewReader(bytes.Repeat([]byte{0x41}, candidateRefBytes*2)),
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := discovery.Scan(context.Background())
	if err != nil || len(first) != 1 {
		t.Fatalf("first scan = %#v, %v", first, err)
	}

	source.mutex.Lock()
	source.err = errors.New("network unavailable")
	source.mutex.Unlock()
	if _, err := discovery.Scan(context.Background()); err == nil {
		t.Fatal("failed rescan succeeded")
	}
	if _, err := discovery.Resolve(first[0].Reference); !errors.Is(err, ErrCandidateNotFound) {
		t.Fatalf("old reference survived failed rescan: %v", err)
	}
}

func TestMDNSSourceScopesQueriesAndRoutesToEligibleInterfaces(t *testing.T) {
	physical := interfaceDescriptor{
		value:    net.Interface{Index: 5, Name: "en0", HardwareAddr: net.HardwareAddr{1, 2, 3, 4, 5, 6}, Flags: net.FlagUp | net.FlagMulticast},
		prefixes: []netip.Prefix{netip.MustParsePrefix("192.168.50.0/24")},
	}
	vpn := interfaceDescriptor{
		value:    net.Interface{Index: 6, Name: "utun4", HardwareAddr: net.HardwareAddr{1, 2, 3, 4, 5, 7}, Flags: net.FlagUp | net.FlagMulticast | net.FlagPointToPoint},
		prefixes: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")},
	}
	public := interfaceDescriptor{
		value:    net.Interface{Index: 7, Name: "en7", HardwareAddr: net.HardwareAddr{1, 2, 3, 4, 5, 8}, Flags: net.FlagUp | net.FlagMulticast},
		prefixes: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")},
	}
	var queried []string
	var queriedMutex sync.Mutex
	source := &MDNSSource{
		listInterfaces: func() ([]interfaceDescriptor, error) { return []interfaceDescriptor{physical, vpn, public}, nil },
		query: func(_ context.Context, descriptor interfaceDescriptor, sink chan<- mdnsEntry) error {
			queriedMutex.Lock()
			queried = append(queried, descriptor.value.Name)
			queriedMutex.Unlock()
			sink <- mdnsEntry{instance: "valid", address: netip.MustParseAddr("192.168.50.44"), port: 3232, txt: []string{"pv=2"}}
			sink <- mdnsEntry{instance: "wrong-subnet", address: netip.MustParseAddr("192.168.60.44"), port: 3232, txt: []string{"pv=2"}}
			return nil
		},
		timeout: time.Second,
	}

	observations, err := source.Browse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(queried)
	if !reflect.DeepEqual(queried, []string{"en0"}) {
		t.Fatalf("queried interfaces = %v", queried)
	}
	if len(observations) != 1 || observations[0].Address.String() != "192.168.50.44" || observations[0].InterfaceName != "en0" {
		t.Fatalf("observations = %#v", observations)
	}
}

func TestMDNSSourceFailsClosedWithoutAnEligibleInterface(t *testing.T) {
	source := &MDNSSource{
		listInterfaces: func() ([]interfaceDescriptor, error) { return nil, nil },
		query: func(context.Context, interfaceDescriptor, chan<- mdnsEntry) error {
			t.Fatal("query must not run")
			return nil
		},
	}
	if _, err := source.Browse(context.Background()); !errors.Is(err, ErrNoUsableInterface) {
		t.Fatalf("error = %v", err)
	}
}
