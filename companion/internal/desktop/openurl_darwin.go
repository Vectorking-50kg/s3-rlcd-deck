//go:build darwin

package desktop

import "os/exec"

func OpenURL(address string) error {
	return exec.Command("open", address).Start()
}
