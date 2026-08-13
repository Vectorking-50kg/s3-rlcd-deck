//go:build !darwin && !windows

package desktop

import "errors"

type NativeTray struct{}

func NewNativeTray() *NativeTray           { return &NativeTray{} }
func (*NativeTray) SetIcon([]byte)         {}
func (*NativeTray) SetTemplateIcon([]byte) {}
func (*NativeTray) SetTooltip(string)      {}
func (*NativeTray) SetStatus(string)       {}
func (*NativeTray) SetRunning(bool)        {}
func (*NativeTray) OnOpenConsole(func())   {}
func (*NativeTray) OnToggleRunning(func()) {}
func (*NativeTray) OnQuit(func())          {}
func (*NativeTray) Show()                  {}
func (*NativeTray) Close()                 {}
func (*NativeTray) Run() error {
	return errors.New("desktop tray is supported only on macOS and Windows")
}
