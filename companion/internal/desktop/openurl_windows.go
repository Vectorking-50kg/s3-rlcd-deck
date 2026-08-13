//go:build windows

package desktop

import (
	"os/exec"
	"syscall"
)

func OpenURL(address string) error {
	command := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", address)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return command.Start()
}
