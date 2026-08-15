package runtime

import (
	"net"
	"testing"
)

func TestResolveDeviceHubAdvertisedAddressFailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		bound      string
		configured string
		routeIP    net.IP
		want       string
	}{
		{name: "explicit", bound: "0.0.0.0:7780", configured: "192.168.1.20:7780", want: "192.168.1.20:7780"},
		{name: "concrete listener", bound: "10.0.0.8:7780", want: "10.0.0.8:7780"},
		{name: "wildcard uses route", bound: "0.0.0.0:7780", routeIP: net.ParseIP("192.168.50.4"), want: "192.168.50.4:7780"},
		{name: "wildcard without route", bound: "0.0.0.0:7780", want: ""},
		{name: "loopback is not advertised", bound: "127.0.0.1:7780", want: ""},
		{name: "invalid explicit fails closed", bound: "10.0.0.8:7780", configured: "0.0.0.0:7780", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resolveDeviceHubAdvertisedAddress(test.bound, test.configured, test.routeIP); got != test.want {
				t.Fatalf("resolveDeviceHubAdvertisedAddress() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeConfigRejectsUnsafeAdvertisedDeviceHubAddress(t *testing.T) {
	config := Config{
		Version: "test",
		Management: ManagementConfig{
			AdminToken: "management-token-with-enough-bytes",
		},
		DeviceHub: DeviceHubConfig{
			AdvertisedAddress: "0.0.0.0:7780",
		},
		Pairing: testPairingService(t),
	}
	if _, err := normalizeConfig(config); err == nil {
		t.Fatal("normalizeConfig accepted an unspecified advertised address")
	}
}

func TestChooseAdvertisedIPv4PrefersPhysicalLANAndRejectsTunnelAddresses(t *testing.T) {
	got := chooseAdvertisedIPv4([]advertisedIPv4Candidate{
		{name: "utun4", flags: net.FlagUp | net.FlagPointToPoint, ip: net.ParseIP("198.18.0.1")},
		{name: "bridge100", flags: net.FlagUp | net.FlagBroadcast, ip: net.ParseIP("192.168.64.1")},
		{name: "en0", flags: net.FlagUp | net.FlagBroadcast, ip: net.ParseIP("192.168.0.101")},
	})
	if !got.Equal(net.ParseIP("192.168.0.101")) {
		t.Fatalf("chooseAdvertisedIPv4() = %v, want physical LAN address", got)
	}
	if got = chooseAdvertisedIPv4([]advertisedIPv4Candidate{{
		name: "utun4", flags: net.FlagUp | net.FlagPointToPoint, ip: net.ParseIP("198.18.0.1"),
	}}); got != nil {
		t.Fatalf("chooseAdvertisedIPv4(tunnel only) = %v, want nil", got)
	}
}
