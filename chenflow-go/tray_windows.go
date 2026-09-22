package main

import (
	_ "embed"
	"sync"

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
	trayQuitCh       = make(chan struct{})
	trayQuitOnce     sync.Once
)

func startTray(onShow, onQuit func()) {
	trayOnShow = onShow
	trayOnQuit = onQuit
	trayStarted = true
	systray.Run(onTrayReady, onTrayExit)
}

func onTrayReady() {
	systray.SetIcon(trayIconBytes)
	systray.SetTitle("NetShunt")
	systray.SetTooltip("NetShunt 网络分流")

	trayMenuItemShow = systray.AddMenuItem("显示窗口", "显示主窗口")
	systray.AddSeparator()
	trayMenuItemQuit = systray.AddMenuItem("退出", "退出程序")

	go func() {
		for {
			select {
			case <-trayQuitCh:
				return
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
		trayQuitOnce.Do(func() { close(trayQuitCh) })
		systray.Quit()
	}
}
