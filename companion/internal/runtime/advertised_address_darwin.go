//go:build darwin

package runtime

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os/exec"
	"regexp"
	"time"
)

const maximumRouteOutputBytes = 8 * 1024

var darwinPhysicalLANInterface = regexp.MustCompile(`^en[0-9]+$`)

type limitedRouteOutput struct {
	buffer   bytes.Buffer
	overflow bool
}

func (output *limitedRouteOutput) Write(data []byte) (int, error) {
	available := maximumRouteOutputBytes - output.buffer.Len()
	if available <= 0 {
		output.overflow = true
		return len(data), nil
	}
	if len(data) > available {
		_, _ = output.buffer.Write(data[:available])
		output.overflow = true
		return len(data), nil
	}
	return output.buffer.Write(data)
}

func platformDefaultRouteInterface(ctx context.Context) (*net.Interface, error) {
	queryContext, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var output limitedRouteOutput
	command := exec.CommandContext(queryContext, "/sbin/route", "-n", "get", "default")
	command.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil || output.overflow {
		return nil, errors.New("default route query failed")
	}
	name, ok := parseDefaultRouteInterface(output.buffer.String())
	if !ok || !darwinPhysicalLANInterface.MatchString(name) {
		return nil, errors.New("default route is not a physical LAN interface")
	}
	return net.InterfaceByName(name)
}
