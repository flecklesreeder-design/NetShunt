package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

func webview2Installed() bool {
	guids := []string{
		`SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`,
		`SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C63A8C4FE2}`,
		`SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`,
		`SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C63A8C4FE2}`,
	}
	for _, g := range guids {
		var hKey syscall.Handle
		if syscall.RegOpenKeyEx(syscall.HKEY_LOCAL_MACHINE, syscall.StringToUTF16Ptr(g), 0, syscall.KEY_READ, &hKey) == nil {
			syscall.RegCloseKey(hKey)
			return true
		}
		if syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, syscall.StringToUTF16Ptr(g), 0, syscall.KEY_READ, &hKey) == nil {
			syscall.RegCloseKey(hKey)
			return true
		}
	}
	return false
}

func showMessageBox(text, caption string, flags uint32) int32 {
	user32 := syscall.NewLazyDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")
	ret, _, _ := messageBox.Call(
		0,
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(text))),
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(caption))),
		uintptr(flags),
	)
	return int32(ret)
}

func findBootstrapper() string {
	exePath, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exePath)
	candidate := filepath.Join(dir, "MicrosoftEdgeWebview2Setup.exe")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

func ensureWebView2() bool {
	if webview2Installed() {
		return true
	}

	bootstrapper := findBootstrapper()

	if bootstrapper != "" {
		msg := "ChenFlow 需要 WebView2 Runtime 才能正常运行，但系统未检测到。\n\n点击「是」立即安装（需要联网下载），安装完成后将自动启动 ChenFlow。\n点击「否」退出程序。"
		ret := showMessageBox(msg, "ChenFlow - WebView2 缺失", 0x00000004|0x00000030)
		if ret != 6 {
			return false
		}
		cmd := exec.Command(bootstrapper, "/silent")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := cmd.Run(); err != nil {
			showMessageBox(fmt.Sprintf("WebView2 安装失败: %v\n\n请手动从微软官网下载安装。", err), "ChenFlow", 0x00000030)
			return false
		}
		for i := 0; i < 30; i++ {
			time.Sleep(time.Second)
			if webview2Installed() {
				return true
			}
		}
		showMessageBox("WebView2 安装似乎未完成，请重试或手动安装。", "ChenFlow", 0x00000030)
		return false
	}

	msg := "ChenFlow 需要 WebView2 Runtime 才能正常运行，但系统未检测到。\n\n请从微软官网下载安装 WebView2 Runtime 后重试：\nhttps://developer.microsoft.com/en-us/microsoft-edge/webview2/\n\n点击「确定」退出程序。"
	showMessageBox(msg, "ChenFlow - WebView2 缺失", 0x00000030)
	return false
}
