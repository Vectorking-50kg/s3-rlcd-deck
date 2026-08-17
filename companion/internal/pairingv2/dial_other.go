//go:build !darwin

package pairingv2

import (
	"context"
	"net"
	"time"
)

func pairingDialContext(_ Route) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: -1}
	return dialer.DialContext
}
