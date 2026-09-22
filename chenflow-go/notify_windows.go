package main

import (
	"fmt"
	"os/exec"
	"strings"
)

func showWindowsNotification(title, message string) {
	ps := fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms
$n = New-Object System.Windows.Forms.NotifyIcon
$n.Icon = [System.Drawing.SystemIcons]::Warning
$n.BalloonTipTitle = '%s'
$n.BalloonTipText = '%s'
$n.BalloonTipIcon = [System.Windows.Forms.ToolTipIcon]::Warning
$n.Visible = $true
$n.ShowBalloonTip(8000)
Start-Sleep -Milliseconds 8500
$n.Dispose()`,
		strings.ReplaceAll(title, "'", "''"),
		strings.ReplaceAll(message, "'", "''"),
	)
	cmd := exec.Command("powershell", "-NoProfile", "-Command", ps)
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}
