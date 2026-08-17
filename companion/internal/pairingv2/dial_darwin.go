//go:build darwin

package pairingv2

import (
	"context"
	"net"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func pairingDialContext(route Route) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: -1,
		Control: func(_, _ string, raw syscall.RawConn) error {
			var controlError error
			if err := raw.Control(func(fileDescriptor uintptr) {
				controlError = unix.SetsockoptInt(
					int(fileDescriptor),
					unix.IPPROTO_IP,
					unix.IP_BOUND_IF,
					route.InterfaceIndex,
				)
			}); err != nil {
				return err
			}
			return controlError
		},
	}
	return dialer.DialContext
}
