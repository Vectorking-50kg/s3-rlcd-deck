//go:build darwin || windows

package desktop

import "github.com/gogpu/systray"

type NativeTray struct {
	tray       *systray.SystemTray
	statusItem *systray.MenuItem
	toggleItem *systray.MenuItem
	menu       *systray.Menu
}

func NewNativeTray() *NativeTray {
	tray := systray.New()
	menu := systray.NewMenu()
	status := menu.Add("Starting", func() {})
	status.SetDisabled(true)
	menu.AddSeparator()
	native := &NativeTray{tray: tray, menu: menu, statusItem: status}
	menu.Add("Open Console", func() {})
	return native
}

func (tray *NativeTray) SetIcon(icon []byte)         { tray.tray.SetIcon(icon) }
func (tray *NativeTray) SetTemplateIcon(icon []byte) { tray.tray.SetTemplateIcon(icon) }
func (tray *NativeTray) SetTooltip(label string)     { tray.tray.SetTooltip(label) }
func (tray *NativeTray) SetStatus(label string)      { tray.statusItem.SetLabel(label) }
func (tray *NativeTray) Show()                       { tray.tray.SetMenu(tray.menu).Show() }
func (tray *NativeTray) Run() error                  { return tray.tray.Run() }
func (tray *NativeTray) Close()                      { tray.tray.Remove() }

func (tray *NativeTray) OnOpenConsole(callback func()) {
	tray.menu = systray.NewMenu()
	tray.statusItem = tray.menu.Add("Starting", func() {})
	tray.statusItem.SetDisabled(true)
	tray.menu.AddSeparator()
	tray.menu.Add("Open Console", callback)
}

func (tray *NativeTray) OnToggleRunning(callback func()) {
	tray.toggleItem = tray.menu.Add("Stop Companion", callback)
}

func (tray *NativeTray) OnQuit(callback func()) {
	tray.menu.AddSeparator()
	tray.menu.Add("Quit", callback)
}

func (tray *NativeTray) SetRunning(running bool) {
	if tray.toggleItem == nil {
		return
	}
	if running {
		tray.toggleItem.SetLabel("Stop Companion")
	} else {
		tray.toggleItem.SetLabel("Start Companion")
	}
}
