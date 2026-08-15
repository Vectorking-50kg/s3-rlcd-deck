//go:build !darwin && !windows

package runtime

import (
	"context"
	"errors"
	"net"
)

func platformDefaultRouteInterface(context.Context) (*net.Interface, error) {
	return nil, errors.New("automatic default-route discovery is unsupported on this platform")
}
