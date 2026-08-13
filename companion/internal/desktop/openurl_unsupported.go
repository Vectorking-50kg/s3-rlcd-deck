//go:build !darwin && !windows

package desktop

import "errors"

func OpenURL(string) error { return errors.New("opening the management console is unsupported") }
