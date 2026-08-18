//go:build !darwin

package pairingv2

import (
	"errors"
	"io"
	"log"
	"net"
	"net/netip"

	mdns "github.com/hashicorp/mdns"
)

type multicastHubAdvertisement struct {
	server *mdns.Server
}

func startPlatformHubAdvertisement(
	instance string,
	port int,
	networkInterface *net.Interface,
	ip netip.Addr,
) (hubAdvertisementBackend, error) {
	zone, err := mdns.NewMDNSService(
		instance,
		HubService,
		PairingDomain,
		"",
		port,
		[]net.IP{net.ParseIP(ip.String())},
		[]string{"pv=2"},
	)
	if err != nil {
		return nil, errors.New("create Pairing v2 Hub advertisement")
	}
	server, err := mdns.NewServer(&mdns.Config{
		Zone:   zone,
		Iface:  networkInterface,
		Logger: log.New(io.Discard, "", 0),
	})
	if err != nil {
		return nil, errors.New("start Pairing v2 Hub advertisement")
	}
	return &multicastHubAdvertisement{server: server}, nil
}

func (advertisement *multicastHubAdvertisement) Close() error {
	return advertisement.server.Shutdown()
}

func (advertisement *multicastHubAdvertisement) Healthy() bool {
	return advertisement != nil && advertisement.server != nil
}
