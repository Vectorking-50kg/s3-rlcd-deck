package pairingv2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"sync"
	"time"

	mdns "github.com/hashicorp/mdns"
)

const (
	defaultBrowseTimeout = 4 * time.Second
	entryBufferSize      = 32
)

type interfaceDescriptor struct {
	value    net.Interface
	prefixes []netip.Prefix
}

type mdnsEntry struct {
	instance string
	address  netip.Addr
	port     int
	txt      []string
}

type interfaceLister func() ([]interfaceDescriptor, error)
type interfaceQuery func(context.Context, interfaceDescriptor, chan<- mdnsEntry) error

type MDNSSource struct {
	listInterfaces interfaceLister
	query          interfaceQuery
	timeout        time.Duration
}

func NewMDNSSource() *MDNSSource {
	return &MDNSSource{
		listInterfaces: listSystemInterfaces,
		query:          queryHashicorpMDNS,
		timeout:        defaultBrowseTimeout,
	}
}

func (source *MDNSSource) Browse(ctx context.Context) ([]Observation, error) {
	interfaces, err := source.listInterfaces()
	if err != nil {
		return nil, fmt.Errorf("list local interfaces: %w", err)
	}
	eligible := make([]interfaceDescriptor, 0, len(interfaces))
	for _, descriptor := range interfaces {
		if eligiblePairingInterface(descriptor) {
			eligible = append(eligible, descriptor)
		}
	}
	if len(eligible) == 0 {
		return nil, ErrNoUsableInterface
	}

	timeout := source.timeout
	if timeout <= 0 {
		timeout = defaultBrowseTimeout
	}
	queryContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type result struct {
		observations []Observation
		err          error
	}
	results := make(chan result, len(eligible))
	var wait sync.WaitGroup
	for _, descriptor := range eligible {
		descriptor := descriptor
		wait.Add(1)
		go func() {
			defer wait.Done()
			entries := make(chan mdnsEntry, entryBufferSize)
			queryDone := make(chan error, 1)
			go func() {
				queryErr := source.query(queryContext, descriptor, entries)
				close(entries)
				queryDone <- queryErr
			}()
			observations := make([]Observation, 0)
			for entry := range entries {
				if !routeBelongsToInterface(entry.address, descriptor.prefixes) || entry.port < 1 || entry.port > 65535 {
					continue
				}
				observations = append(observations, Observation{
					Instance:       entry.instance,
					InterfaceIndex: descriptor.value.Index,
					InterfaceName:  descriptor.value.Name,
					Address:        entry.address.Unmap(),
					Port:           uint16(entry.port),
					TXT:            append([]string(nil), entry.txt...),
				})
			}
			results <- result{observations: observations, err: <-queryDone}
		}()
	}
	wait.Wait()
	close(results)

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	observations := make([]Observation, 0)
	successes := 0
	errorsSeen := make([]error, 0)
	for queryResult := range results {
		if queryResult.err != nil && !errors.Is(queryResult.err, context.DeadlineExceeded) && !errors.Is(queryResult.err, context.Canceled) {
			errorsSeen = append(errorsSeen, queryResult.err)
			continue
		}
		successes++
		observations = append(observations, queryResult.observations...)
	}
	if successes == 0 && len(errorsSeen) != 0 {
		return nil, fmt.Errorf("query Pairing v2 DNS-SD: %w", errors.Join(errorsSeen...))
	}
	return observations, nil
}

func listSystemInterfaces() ([]interfaceDescriptor, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	descriptors := make([]interfaceDescriptor, 0, len(interfaces))
	for _, networkInterface := range interfaces {
		addresses, err := networkInterface.Addrs()
		if err != nil {
			continue
		}
		prefixes := make([]netip.Prefix, 0, len(addresses))
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err != nil || !usablePairingIPv4(prefix.Addr()) {
				continue
			}
			prefixes = append(prefixes, prefix.Masked())
		}
		descriptors = append(descriptors, interfaceDescriptor{value: networkInterface, prefixes: prefixes})
	}
	return descriptors, nil
}

func eligiblePairingInterface(descriptor interfaceDescriptor) bool {
	flags := descriptor.value.Flags
	if flags&net.FlagUp == 0 || flags&net.FlagMulticast == 0 ||
		flags&net.FlagLoopback != 0 || flags&net.FlagPointToPoint != 0 {
		return false
	}
	if len(descriptor.value.HardwareAddr) < 6 || allZero(descriptor.value.HardwareAddr) {
		return false
	}
	for _, prefix := range descriptor.prefixes {
		if usablePairingIPv4(prefix.Addr()) {
			return true
		}
	}
	return false
}

func routeBelongsToInterface(address netip.Addr, prefixes []netip.Prefix) bool {
	address = address.Unmap()
	if !usablePairingIPv4(address) {
		return false
	}
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func allZero(value []byte) bool {
	for _, part := range value {
		if part != 0 {
			return false
		}
	}
	return true
}

func queryHashicorpMDNS(ctx context.Context, descriptor interfaceDescriptor, entries chan<- mdnsEntry) error {
	results := make(chan *mdns.ServiceEntry, entryBufferSize)
	params := &mdns.QueryParam{
		Service:     PairingService,
		Domain:      PairingDomain,
		Timeout:     defaultBrowseTimeout,
		Interface:   &descriptor.value,
		Entries:     results,
		DisableIPv6: true,
		Logger:      log.New(io.Discard, "", 0),
	}
	done := make(chan error, 1)
	go func() {
		err := mdns.QueryContext(ctx, params)
		close(results)
		done <- err
	}()
	for result := range results {
		if result == nil {
			continue
		}
		address, valid := netip.AddrFromSlice(result.AddrV4)
		if !valid {
			continue
		}
		entry := mdnsEntry{
			instance: result.Name,
			address:  address.Unmap(),
			port:     result.Port,
			txt:      append([]string(nil), result.InfoFields...),
		}
		select {
		case entries <- entry:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return <-done
}
