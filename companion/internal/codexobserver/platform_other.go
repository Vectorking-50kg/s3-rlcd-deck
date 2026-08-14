//go:build !darwin && !windows

package codexobserver

import "context"

type unsupportedPlatform struct{}

func newPlatformDiscoverer() platformDiscoverer { return unsupportedPlatform{} }

func (unsupportedPlatform) discover(
	context.Context,
	[]string,
) (mappingStrength, []processObservation, error) {
	return mappingWeak, []processObservation{}, nil
}
