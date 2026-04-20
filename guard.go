//go:build windows

package main

import (
    "fmt"
    "os/exec"
    "syscall"
    "time"
)

// 关闭系统代理
func disableSystemProxy() {
    cmd := exec.Command("powershell", "-Command",
        `Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyEnable -Value 0`)
    cmd.SysProcAttr = &syscall.SysProcAttr{
        HideWindow:    true,
        CreationFlags: 0x08000000,
    }
    _ = cmd.Run()
}

// 结束 mihomo 进程（保险）
func killMihomo() {
    _ = exec.Command("taskkill", "/IM", "mihomo.exe", "/F").Run()
}

func ctrlHandler(ctrlType uint32) bool {
    switch ctrlType {
    case syscall.CTRL_SHUTDOWN_EVENT,
        syscall.CTRL_LOGOFF_EVENT,
        syscall.CTRL_CLOSE_EVENT:
        fmt.Println("NetTrayGuard: 检测到关机/重启/注销，开始清理系统代理和 mihomo")
        disableSystemProxy()
        killMihomo()
        // 返回 true 表示已处理
        return true
    }
    return false
}

func main() {
    // 注册控制台控制事件处理函数
    _ = syscall.SetConsoleCtrlHandler(ctrlHandler, true)

    // 守护进程常驻，不退出
    for {
        time.Sleep(10 * time.Second)
    }
}
