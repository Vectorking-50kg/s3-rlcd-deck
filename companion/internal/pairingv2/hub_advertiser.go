package pairingv2

import (
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
)

type hubAdvertisementBackend interface {
	Close() error
	Healthy() bool
}

// HubAdvertisement owns one interface-scoped DNS-SD publisher. Its records
// carry locator data only; Device Link authority remains the pinned certificate
// and per-Deck bearer Token.
type HubAdvertisement struct {
	backend   hubAdvertisementBackend
	service   string
	address   string
	closeOnce sync.Once
	closeErr  error
}

func StartHubAdvertisement(service string, address string) (*HubAdvertisement, error) {
	if !ValidHubService(service) {
		return nil, errors.New("invalid Pairing v2 Hub service")
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("invalid Pairing v2 Hub address")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !usablePairingIPv4(ip) {
		return nil, errors.New("invalid Pairing v2 Hub address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid Pairing v2 Hub port")
	}
	networkInterface, err := interfaceForPairingAddress(ip.Unmap())
	if err != nil {
		return nil, err
	}
	suffix := "." + HubService + "." + PairingDomain + "."
	instance := strings.TrimSuffix(service, suffix)
	if instance == service || instance == "" {
		return nil, errors.New("invalid Pairing v2 Hub instance")
	}
	backend, err := startPlatformHubAdvertisement(instance, port, networkInterface, ip.Unmap())
	if err != nil {
		return nil, err
	}
	return &HubAdvertisement{backend: backend, service: service, address: address}, nil
}

func (advertisement *HubAdvertisement) Service() string {
	if advertisement == nil {
		return ""
	}
	return advertisement.service
}

func (advertisement *HubAdvertisement) Address() string {
	if advertisement == nil || !advertisement.Healthy() {
		return ""
	}
	return advertisement.address
}

func (advertisement *HubAdvertisement) Healthy() bool {
	return advertisement != nil && advertisement.backend != nil && advertisement.backend.Healthy()
}

func (advertisement *HubAdvertisement) Close() error {
	if advertisement == nil {
		return nil
	}
	advertisement.closeOnce.Do(func() {
		advertisement.closeErr = advertisement.backend.Close()
	})
	return advertisement.closeErr
}

func interfaceForPairingAddress(target netip.Addr) (*net.Interface, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, errors.New("list Pairing v2 Hub interfaces")
	}
	var selected *net.Interface
	for index := range interfaces {
		networkInterface := interfaces[index]
		if networkInterface.Flags&net.FlagUp == 0 ||
			networkInterface.Flags&net.FlagMulticast == 0 ||
			networkInterface.Flags&net.FlagLoopback != 0 ||
			networkInterface.Flags&net.FlagPointToPoint != 0 ||
			len(networkInterface.HardwareAddr) < 6 || allZero(networkInterface.HardwareAddr) {
			continue
		}
		addresses, addressErr := networkInterface.Addrs()
		if addressErr != nil {
			continue
		}
		for _, address := range addresses {
			prefix, parseErr := netip.ParsePrefix(address.String())
			if parseErr != nil || prefix.Addr().Unmap() != target {
				continue
			}
			if selected != nil {
				return nil, errors.New("ambiguous Pairing v2 Hub interface")
			}
			selected = &networkInterface
			break
		}
	}
	if selected == nil {
		return nil, ErrNoUsableInterface
	}
	return selected, nil
}
