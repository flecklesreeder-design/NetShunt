package main

import (
	_ "embed"

	"github.com/getlantern/systray"
)

//go:embed build/windows/icon.ico
var trayIconBytes []byte

var (
	trayMenuItemShow *systray.MenuItem
	trayMenuItemQuit *systray.MenuItem
	trayOnShow       func()
	trayOnQuit       func()
	trayStarted      bool
)

func startTray(onShow, onQuit func()) {
	trayOnShow = onShow
	trayOnQuit = onQuit
	trayStarted = true
	systray.Run(onTrayReady, onTrayExit)
}

func onTrayReady() {
	systray.SetIcon(trayIconBytes)
	systray.SetTitle("ChenFlow")
	systray.SetTooltip("ChenFlow 网络分流")

	trayMenuItemShow = systray.AddMenuItem("显示窗口", "显示主窗口")
	systray.AddSeparator()
	trayMenuItemQuit = systray.AddMenuItem("退出", "退出程序")

	go func() {
		for {
			select {
			case <-trayMenuItemShow.ClickedCh:
				if trayOnShow != nil {
					trayOnShow()
				}
			case <-trayMenuItemQuit.ClickedCh:
				if trayOnQuit != nil {
					trayOnQuit()
				}
			}
		}
	}()
}

func onTrayExit() {
	trayStarted = false
}

func stopTray() {
	if trayStarted {
		systray.Quit()
	}
}
