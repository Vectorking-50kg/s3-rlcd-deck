//go:build darwin

package pairingv2

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"testing"
)

func TestNativeBonjourPublishesAndReleasesTheHubService(t *testing.T) {
	if os.Getenv("S3DECK_NATIVE_BONJOUR_TEST") != "1" {
		t.Skip("native Bonjour proof is enabled only by the macOS workflow")
	}
	address := nativeBonjourTestAddress(t)
	service := fmt.Sprintf("s3deck-native-%d._s3rlcd-hub._tcp.local.", os.Getpid())
	for attempt := 0; attempt < 2; attempt++ {
		advertisement, err := StartHubAdvertisement(service, address)
		if err != nil {
			t.Fatalf("StartHubAdvertisement(attempt %d): %v", attempt+1, err)
		}
		if !advertisement.Healthy() || advertisement.Address() != address ||
			advertisement.Service() != service {
			t.Fatalf("unhealthy native advertisement: %#v", advertisement)
		}
		if err = advertisement.Close(); err != nil {
			t.Fatalf("Close(attempt %d): %v", attempt+1, err)
		}
		if advertisement.Healthy() || advertisement.Address() != "" {
			t.Fatal("closed native advertisement remained usable")
		}
	}
}

func nativeBonjourTestAddress(t *testing.T) string {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for index := range interfaces {
		networkInterface := interfaces[index]
		if networkInterface.Flags&net.FlagUp == 0 ||
			networkInterface.Flags&net.FlagMulticast == 0 ||
			networkInterface.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 ||
			len(networkInterface.HardwareAddr) < 6 {
			continue
		}
		addresses, addressErr := networkInterface.Addrs()
		if addressErr != nil {
			continue
		}
		for _, address := range addresses {
			prefix, parseErr := netip.ParsePrefix(address.String())
			if parseErr == nil && usablePairingIPv4(prefix.Addr()) {
				return net.JoinHostPort(prefix.Addr().Unmap().String(), "7780")
			}
		}
	}
	t.Fatal("macOS native runner has no eligible physical LAN address")
	return ""
}
