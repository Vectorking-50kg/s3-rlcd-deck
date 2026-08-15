//go:build !darwin && !windows

package installation

import "context"

type unsupportedAdapter struct{}

func newPlatformAdapter(string) platformAdapter                         { return &unsupportedAdapter{} }
func DefaultRootDirectory() (string, error)                             { return "", ErrUnavailable }
func (*unsupportedAdapter) Name() string                                { return "unsupported" }
func (*unsupportedAdapter) Configure(context.Context, launchSpec) error { return ErrPlatform }
func (*unsupportedAdapter) SetEnabled(context.Context, bool) error      { return ErrPlatform }
func (*unsupportedAdapter) Remove(context.Context) error                { return ErrPlatform }
func (*unsupportedAdapter) Status(context.Context) (platformStatus, error) {
	return platformStatus{}, ErrPlatform
}
