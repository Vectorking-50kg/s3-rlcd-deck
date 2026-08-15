package runtime

import (
	"net"
	"sort"
	"strconv"
	"strings"
)

type advertisedIPv4Candidate struct {
	name  string
	flags net.Flags
	ip    net.IP
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

func usableAdvertisedIPv4(ip net.IP) bool {
	ipv4 := ip.To4()
	return ipv4 != nil && !ipv4.IsUnspecified() && !ipv4.IsLoopback() &&
		!ipv4.IsMulticast() && !ipv4.IsLinkLocalUnicast()
}

func preferredAdvertisedIPv4() net.IP {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	candidates := make([]advertisedIPv4Candidate, 0, len(interfaces))
	for _, networkInterface := range interfaces {
		addresses, addressErr := networkInterface.Addrs()
		if addressErr != nil {
			continue
		}
		for _, address := range addresses {
			var ip net.IP
			switch value := address.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			if ip != nil {
				candidates = append(candidates, advertisedIPv4Candidate{
					name: networkInterface.Name, flags: networkInterface.Flags, ip: ip,
				})
			}
		}
	}
	return chooseAdvertisedIPv4(candidates)
}

func chooseAdvertisedIPv4(candidates []advertisedIPv4Candidate) net.IP {
	type rankedCandidate struct {
		score int
		name  string
		ip    net.IP
	}
	ranked := make([]rankedCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.flags&net.FlagUp == 0 || candidate.flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 ||
			!usableAdvertisedIPv4(candidate.ip) || benchmarkIPv4(candidate.ip) {
			continue
		}
		name := strings.ToLower(candidate.name)
		if strings.HasPrefix(name, "utun") || strings.HasPrefix(name, "tun") ||
			strings.HasPrefix(name, "tap") || strings.HasPrefix(name, "docker") ||
			strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "br-") ||
			strings.HasPrefix(name, "bridge") || strings.HasPrefix(name, "tailscale") ||
			strings.HasPrefix(name, "awdl") || strings.HasPrefix(name, "llw") {
			continue
		}
		score := 10
		if candidate.ip.IsPrivate() {
			score += 100
		}
		if name == "en0" || strings.HasPrefix(name, "eth") || strings.HasPrefix(name, "enp") ||
			strings.HasPrefix(name, "eno") || strings.HasPrefix(name, "ens") ||
			strings.HasPrefix(name, "wlan") || strings.HasPrefix(name, "wlp") ||
			strings.Contains(name, "ethernet") || strings.Contains(name, "wi-fi") ||
			strings.Contains(name, "wifi") {
			score += 200
		}
		ipv4 := append(net.IP(nil), candidate.ip.To4()...)
		ranked = append(ranked, rankedCandidate{score: score, name: name, ip: ipv4})
	}
	if len(ranked) == 0 {
		return nil
	}
	sort.Slice(ranked, func(left, right int) bool {
		if ranked[left].score != ranked[right].score {
			return ranked[left].score > ranked[right].score
		}
		if ranked[left].name != ranked[right].name {
			return ranked[left].name < ranked[right].name
		}
		return ranked[left].ip.String() < ranked[right].ip.String()
	})
	return ranked[0].ip
}

func benchmarkIPv4(ip net.IP) bool {
	ipv4 := ip.To4()
	return ipv4 != nil && ipv4[0] == 198 && (ipv4[1] == 18 || ipv4[1] == 19)
}
