package main

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32            = syscall.NewLazyDLL("user32.dll")
	pRegisterHotKey   = user32.NewProc("RegisterHotKey")
	pUnregisterHotKey = user32.NewProc("UnregisterHotKey")
	pPeekMessage      = user32.NewProc("PeekMessageW")
	pTranslateMessage = user32.NewProc("TranslateMessage")
	pDispatchMessage  = user32.NewProc("DispatchMessageW")
)

const (
	wmHotKey    = 0x0312
	modAlt      = 0x0001
	modCtrl     = 0x0002
	modShift    = 0x0004
	modWin      = 0x0008
	modNoRepeat = 0x4000
	hotkeyID    = 1
)

type tmsg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      [2]int32
}

type hotkeyConfig struct {
	mods int
	vk   int
}

type hotkeyRequest struct {
	cfg   hotkeyConfig
	reply chan bool
}

var (
	currentHotkey  = hotkeyConfig{mods: modCtrl | modAlt, vk: 0x48}
	hotkeyMu       sync.RWMutex
	hotkeyReqCh    = make(chan hotkeyRequest, 1)
	hotkeyQuitCh   = make(chan struct{})
	hotkeyQuitOnce sync.Once
)

func stopHotkey() {
	hotkeyQuitOnce.Do(func() { close(hotkeyQuitCh) })
}

const defaultHotkey = "ctrl+alt+h"

func registerHotKey() bool {
	hotkeyMu.RLock()
	mods := currentHotkey.mods
	vk := currentHotkey.vk
	hotkeyMu.RUnlock()
	r1, _, _ := pRegisterHotKey.Call(0, hotkeyID, uintptr(mods|modNoRepeat), uintptr(vk))
	return r1 != 0
}

func unregisterHotKey() {
	pUnregisterHotKey.Call(0, hotkeyID)
}

func setHotkey(mods, vk int) bool {
	reply := make(chan bool, 1)
	req := hotkeyRequest{cfg: hotkeyConfig{mods: mods, vk: vk}, reply: reply}
	select {
	case hotkeyReqCh <- req:
	default:
		select {
		case <-hotkeyReqCh:
		default:
		}
		hotkeyReqCh <- req
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case ok := <-reply:
		return ok
	case <-timer.C:
		return false
	}
}

func hotkeyLoop(onTrigger func()) {
	runtime.LockOSThread()
	if !registerHotKey() {
		return
	}
	defer unregisterHotKey()
	var m tmsg
	var lastTrigger time.Time
	for {
		select {
		case <-hotkeyQuitCh:
			return
		case req := <-hotkeyReqCh:
			unregisterHotKey()
			hotkeyMu.Lock()
			currentHotkey = req.cfg
			hotkeyMu.Unlock()
			req.reply <- registerHotKey()
		default:
		}

		has, _, _ := pPeekMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, 1)
		if has != 0 {
			if m.Message == wmHotKey {
				for {
					h2, _, _ := pPeekMessage.Call(uintptr(unsafe.Pointer(&m)), 0, wmHotKey, wmHotKey, 1)
					if h2 == 0 {
						break
					}
				}
				now := time.Now()
				if now.Sub(lastTrigger) >= 300*time.Millisecond {
					lastTrigger = now
					onTrigger()
				}
			}
			pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func parseHotkey(s string) (mods, vk int, ok bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, 0, false
	}
	parts := strings.Split(s, "+")
	mods = 0
	vk = 0
	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch p {
		case "ctrl", "control":
			mods |= modCtrl
		case "alt":
			mods |= modAlt
		case "shift":
			mods |= modShift
		case "win", "super":
			mods |= modWin
		default:
			if len(p) == 1 && p[0] >= 'a' && p[0] <= 'z' {
				vk = int(p[0]) - 32
			} else if len(p) == 1 && p[0] >= '0' && p[0] <= '9' {
				vk = int(p[0]) - '0' + 0x30
			} else if strings.HasPrefix(p, "f") {
				var n int
				if _, err := fmt.Sscanf(p[1:], "%d", &n); err == nil && n >= 1 && n <= 12 {
					vk = 0x6F + n
				}
			}
		}
	}
	if vk == 0 || mods == 0 {
		return 0, 0, false
	}
	return mods, vk, true
}

func hotkeyToString(mods, vk int) string {
	var parts []string
	if mods&modCtrl != 0 {
		parts = append(parts, "Ctrl")
	}
	if mods&modAlt != 0 {
		parts = append(parts, "Alt")
	}
	if mods&modShift != 0 {
		parts = append(parts, "Shift")
	}
	if mods&modWin != 0 {
		parts = append(parts, "Win")
	}
	if vk >= 0x41 && vk <= 0x5A {
		parts = append(parts, string(rune(vk+32)))
	} else if vk >= 0x30 && vk <= 0x39 {
		parts = append(parts, string(rune(vk)))
	} else if vk >= 0x70 && vk <= 0x7B {
		parts = append(parts, "F"+fmt.Sprintf("%d", vk-0x6F))
	}
	return strings.Join(parts, "+")
}
