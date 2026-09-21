package utils

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

var (
	ipv4Re     = regexp.MustCompile(`^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$`)
	ipv6Re     = regexp.MustCompile(`^[0-9a-fA-F:]+$`)
	hostnameRe = regexp.MustCompile(
		`^[a-zA-Z0-9]([a-zA-Z0-9\-]*[a-zA-Z0-9])?` +
			`(\.[a-zA-Z0-9]([a-zA-Z0-9\-]*[a-zA-Z0-9])?)*\.[a-zA-Z]{2,}$`)
	cmdSafeRe = regexp.MustCompile(`^[0-9a-zA-Z\.\-:/]+$`)
)

// HideWindow 隐藏命令行窗口（Windows GUI 应用调用 cmd/powershell 时必须设置）
func HideWindow(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

// DecodeGBK 将 GBK 编码的字节解码为 UTF-8 字符串
func DecodeGBK(b []byte) string {
	decoder := simplifiedchinese.GBK.NewDecoder()
	decoded, err := decoder.Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(decoded)
}

// RunCmd 执行系统命令并返回输出（自动 GBK→UTF-8 解码，带超时）
func RunCmd(cmd string, timeoutSec int) string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "cmd", "/c", cmd)
	HideWindow(c)
	out, err := c.CombinedOutput()
	if err != nil {
		if len(out) > 0 {
			return DecodeGBK(out)
		}
		return ""
	}
	return DecodeGBK(out)
}

// RunCmdStreaming 执行系统命令，逐行实时回调 onLine（自动 GBK→UTF-8 解码，带超时）。
// 命令结束后回调 onDone（err 为 nil 表示正常结束）。
func RunCmdStreaming(cmd string, timeoutSec int, onLine func(string), onDone func(error)) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "cmd", "/c", cmd)
	HideWindow(c)

	stdout, err := c.StdoutPipe()
	if err != nil {
		onDone(err)
		return
	}
	c.Stderr = c.Stdout

	if err := c.Start(); err != nil {
		onDone(err)
		return
	}

	decoder := simplifiedchinese.GBK.NewDecoder()
	reader := decoder.Reader(stdout)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	scanDone := make(chan struct{})
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if onLine != nil {
				onLine(line)
			}
		}
		close(scanDone)
	}()

	err = c.Wait()
	<-scanDone

	if ctx.Err() == context.DeadlineExceeded {
		err = ctx.Err()
	}
	if onDone != nil {
		onDone(err)
	}
}

// RunPSStreaming 执行 PowerShell 命令，逐行实时回调 onLine（自动 GBK→UTF-8 解码，带超时）。
// 直接调 powershell.exe（不经 cmd /c），避免 cmd 拦截 PowerShell 管道符 |。
func RunPSStreaming(psScript string, timeoutSec int, onLine func(string), onDone func(error)) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", psScript)
	HideWindow(c)

	stdout, err := c.StdoutPipe()
	if err != nil {
		onDone(err)
		return
	}
	c.Stderr = c.Stdout

	if err := c.Start(); err != nil {
		onDone(err)
		return
	}

	decoder := simplifiedchinese.GBK.NewDecoder()
	reader := decoder.Reader(stdout)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	scanDone := make(chan struct{})
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if onLine != nil {
				onLine(line)
			}
		}
		close(scanDone)
	}()

	err = c.Wait()
	<-scanDone

	if ctx.Err() == context.DeadlineExceeded {
		err = ctx.Err()
	}
	if onDone != nil {
		onDone(err)
	}
}

// RunPS 执行 PowerShell 命令并返回输出（自动 GBK→UTF-8 解码，30秒超时）
func RunPS(psScript string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", psScript)
	HideWindow(c)
	out, err := c.Output()
	if err != nil {
		if len(out) > 0 {
			return DecodeGBK(out)
		}
		return ""
	}
	return DecodeGBK(out)
}

// GetAppDir 程序运行目录
func GetAppDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	idx := strings.LastIndexByte(exe, os.PathSeparator)
	if idx < 0 {
		return "."
	}
	return exe[:idx]
}

// FormatBytes 格式化字节数
func FormatBytes(size float64) string {
	power := 1024.0
	n := 0
	labels := []string{"B", "KB", "MB", "GB", "TB"}
	for size > power && n < 4 {
		size /= power
		n++
	}
	return fmt.Sprintf("%.2f %s", size, labels[n])
}

// IsValidIPv4 校验 IPv4
func IsValidIPv4(text string) bool {
	m := ipv4Re.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return false
	}
	for _, g := range m[1:] {
		var v int
		fmt.Sscanf(g, "%d", &v)
		if v < 0 || v > 255 {
			return false
		}
	}
	return true
}

// IsValidIPv6 校验 IPv6
func IsValidIPv6(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" || !strings.Contains(t, ":") {
		return false
	}
	return ipv6Re.MatchString(t)
}

// IsValidHostname 校验域名
func IsValidHostname(text string) bool {
	t := strings.TrimSpace(text)
	return hostnameRe.MatchString(t) && len(t) <= 253
}

// ClassifyTarget 返回 IPv4 / IPv6 / URL / 无效
func ClassifyTarget(text string) string {
	t := strings.TrimSpace(text)
	if IsValidIPv4(t) {
		return "IPv4"
	}
	if IsValidIPv6(t) {
		return "IPv6"
	}
	if IsValidHostname(t) {
		return "URL"
	}
	return "无效"
}

// SanitizeForCmd 命令白名单校验
func SanitizeForCmd(text string) bool {
	return cmdSafeRe.MatchString(strings.TrimSpace(text))
}

// CIDRToNetmask CIDR 转子网掩码
func CIDRToNetmask(cidrBits int) string {
	mask := (0xFFFFFFFF >> (32 - cidrBits)) << (32 - cidrBits)
	return fmt.Sprintf("%d.%d.%d.%d",
		(mask>>24)&0xFF, (mask>>16)&0xFF, (mask>>8)&0xFF, mask&0xFF)
}

// CmdFailed 判断 route 命令输出是否表示失败（"对象已存在"不算失败）
func CmdFailed(output string) bool {
	text := strings.ToLower(output)
	for _, m := range []string{"对象已存在", "already exists", "already exist", "ok!"} {
		if strings.Contains(text, m) {
			return false
		}
	}
	for _, k := range []string{"错误", "error", "无法", "失败", "提升", "elevation", "denied", "拒绝"} {
		if strings.Contains(output, k) || strings.Contains(text, k) {
			return true
		}
	}
	return false
}
