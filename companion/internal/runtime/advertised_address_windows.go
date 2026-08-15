//go:build windows

package runtime

import (
	"context"
	"errors"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

func platformDefaultRouteInterface(_ context.Context) (*net.Interface, error) {
	var table *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(windows.AF_INET, &table); err != nil {
		return nil, err
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))

	var selectedIndex uint32
	var selectedMetric uint64
	for _, route := range table.Rows() {
		if route.DestinationPrefix.PrefixLength != 0 || route.InterfaceIndex == 0 {
			continue
		}
		interfaceRow := windows.MibIpInterfaceRow{
			Family:         windows.AF_INET,
			InterfaceLuid:  route.InterfaceLuid,
			InterfaceIndex: route.InterfaceIndex,
		}
		if err := windows.GetIpInterfaceEntry(&interfaceRow); err != nil || interfaceRow.DisableDefaultRoutes != 0 {
			continue
		}
		metric := uint64(route.Metric) + uint64(interfaceRow.Metric)
		if selectedIndex == 0 || metric < selectedMetric {
			selectedIndex = route.InterfaceIndex
			selectedMetric = metric
			continue
		}
		if metric == selectedMetric && selectedIndex != route.InterfaceIndex {
			return nil, errors.New("default route is ambiguous")
		}
	}
	if selectedIndex == 0 {
		return nil, errors.New("default route is unavailable")
	}
	interfaceRow := windows.MibIfRow2{InterfaceIndex: selectedIndex}
	if err := windows.GetIfEntry2Ex(windows.MibIfTableNormalWithoutStatistics, &interfaceRow); err != nil {
		return nil, err
	}
	if interfaceRow.OperStatus != windows.IfOperStatusUp ||
		(interfaceRow.Type != windows.IF_TYPE_ETHERNET_CSMACD && interfaceRow.Type != windows.IF_TYPE_IEEE80211) {
		return nil, errors.New("default route is not a physical LAN interface")
	}
	return net.InterfaceByIndex(int(selectedIndex))
}
