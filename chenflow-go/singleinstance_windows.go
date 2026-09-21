package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const singleInstanceMutexName = "NetShunt_SingleInstance_Mutex_v3"

var singleInstanceMutex windows.Handle

func ensureSingleInstance() bool {
	name, _ := windows.UTF16PtrFromString(singleInstanceMutexName)
	handle, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		if err == windows.ERROR_ALREADY_EXISTS {
			if handle != 0 {
				windows.CloseHandle(handle)
			}
			return false
		}
		return true
	}
	singleInstanceMutex = handle
	return true
}

func closeSingleInstanceMutex() {
	if singleInstanceMutex != 0 {
		windows.CloseHandle(singleInstanceMutex)
		singleInstanceMutex = 0
	}
}

func killExistingInstance() {
	hwnd := findNetShuntWindow()
	if hwnd != 0 {
		sendCloseMessage(hwnd)
	}
}

func findNetShuntWindow() uintptr {
	var found uintptr

	user32 := syscall.NewLazyDLL("user32.dll")
	enumWindows := user32.NewProc("EnumWindows")
	getWindowTextW := user32.NewProc("GetWindowTextW")
	isWindowVisible := user32.NewProc("IsWindowVisible")

	cb := syscall.NewCallback(func(hwnd uintptr, lparam uintptr) uintptr {
		var title [256]uint16
		getWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&title[0])), 256)
		winTitle := windows.UTF16ToString(title[:])
		visible, _, _ := isWindowVisible.Call(hwnd)
		if visible != 0 && winTitle == "NetShunt" {
			found = hwnd
			return 0
		}
		return 1
	})

	enumWindows.Call(cb, 0)
	return found
}

func sendCloseMessage(hwnd uintptr) {
	user32 := syscall.NewLazyDLL("user32.dll")
	postMessageW := user32.NewProc("PostMessageW")
	_, _, _ = postMessageW.Call(hwnd, 0x0010, 0, 0)
}

func bringWindowToFront() bool {
	hwnd := findNetShuntWindow()
	if hwnd == 0 {
		return false
	}
	user32 := syscall.NewLazyDLL("user32.dll")
	showWindow := user32.NewProc("ShowWindow")
	setForegroundWindow := user32.NewProc("SetForegroundWindow")
	showWindow.Call(hwnd, 9)
	setForegroundWindow.Call(hwnd)
	return true
}

func singleInstanceWarnAndExit() {
	fmt.Fprintln(os.Stderr, "另一个 NetShunt 实例正在运行，已尝试将其关闭。")
}
