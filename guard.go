//go:build windows

package main

import (
    "fmt"
    "os/exec"
    "syscall"
    "time"
)

func disableSystemProxy() {
    cmd := exec.Command("powershell", "-Command",
        `Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyEnable -Value 0`)
    cmd.SysProcAttr = &syscall.SysProcAttr{
        HideWindow:    true,
        CreationFlags: 0x08000000,
    }
    _ = cmd.Run()
}

func killMihomo() {
    _ = exec.Command("taskkill", "/IM", "mihomo.exe", "/F").Run()
}

func handler(ctrlType uint32) bool {
    switch ctrlType {
    case syscall.CTRL_SHUTDOWN_EVENT,
        syscall.CTRL_LOGOFF_EVENT,
        syscall.CTRL_CLOSE_EVENT:
        fmt.Println("NetTrayGuard: 检测到关机/重启/注销 → 清理系统代理")
        disableSystemProxy()
        killMihomo()
        return true
    }
    return false
}

func main() {
    syscall.SetConsoleCtrlHandler(handler, true)

    for {
        time.Sleep(10 * time.Second)
    }
}
