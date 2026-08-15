package runtime

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

var reservedAdvertisedIPv4Prefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

func validAdvertisedDeviceHubAddress(address string) bool {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	ip := net.ParseIP(host)
	return err == nil && port != 0 && usableAdvertisedIPv4(ip)
}

func resolveDeviceHubAdvertisedAddress(boundAddress string, configured string, routeIP net.IP) string {
	if configured != "" {
		if validAdvertisedDeviceHubAddress(configured) {
			return configured
		}
		return ""
	}
	host, port, err := net.SplitHostPort(boundAddress)
	if err != nil {
		return ""
	}
	boundIP := net.ParseIP(host)
	if usableAdvertisedIPv4(boundIP) {
		return net.JoinHostPort(boundIP.String(), port)
	}
	if boundIP == nil || !boundIP.IsUnspecified() || !usableAdvertisedIPv4(routeIP) {
		return ""
	}
	return net.JoinHostPort(routeIP.String(), port)
}

func liveDeviceHubAdvertisedAddress(ctx context.Context, boundAddress string, configured string) string {
	if configured != "" {
		return resolveDeviceHubAdvertisedAddress(boundAddress, configured, nil)
	}
	host, _, err := net.SplitHostPort(boundAddress)
	if err != nil {
		return ""
	}
	boundIP := net.ParseIP(host)
	if usableAdvertisedIPv4(boundIP) {
		return resolveDeviceHubAdvertisedAddress(boundAddress, "", nil)
	}
	if boundIP == nil || !boundIP.IsUnspecified() {
		return ""
	}
	routeIP, err := defaultRouteIPv4(ctx)
	if err != nil {
		return ""
	}
	return resolveDeviceHubAdvertisedAddress(boundAddress, "", routeIP)
}

func usableAdvertisedIPv4(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if !address.Is4() || !address.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range reservedAdvertisedIPv4Prefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func defaultRouteIPv4(ctx context.Context) (net.IP, error) {
	networkInterface, err := platformDefaultRouteInterface(ctx)
	if err != nil || networkInterface == nil {
		return nil, errors.New("default route is unavailable")
	}
	if networkInterface.Flags&net.FlagUp == 0 ||
		networkInterface.Flags&net.FlagBroadcast == 0 ||
		networkInterface.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 ||
		len(networkInterface.HardwareAddr) < 6 {
		return nil, errors.New("default route is not a physical LAN interface")
	}
	addresses, err := networkInterface.Addrs()
	if err != nil {
		return nil, err
	}
	var selected net.IP
	for _, address := range addresses {
		var ip net.IP
		switch value := address.(type) {
		case *net.IPNet:
			ip = value.IP
		case *net.IPAddr:
			ip = value.IP
		}
		if !usableAdvertisedIPv4(ip) {
			continue
		}
		if selected != nil && !selected.Equal(ip) {
			return nil, errors.New("default route has ambiguous IPv4 addresses")
		}
		selected = append(net.IP(nil), ip.To4()...)
	}
	if selected == nil {
		return nil, errors.New("default route has no usable IPv4 address")
	}
	return selected, nil
}

func parseDefaultRouteInterface(output string) (string, bool) {
	var selected string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "interface:" {
			continue
		}
		if selected != "" && selected != fields[1] {
			return "", false
		}
		selected = fields[1]
	}
	return selected, selected != ""
}
