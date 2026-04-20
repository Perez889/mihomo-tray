package main

import (
    "os/exec"
    "syscall"
    "time"
    "fmt"
)

func disableSystemProxy() {
    cmd := exec.Command("powershell", "-Command",
        `Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyEnable -Value 0`)
    cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
    _ = cmd.Run()
}

func killMihomo() {
    exec.Command("taskkill", "/IM", "mihomo.exe", "/F").Run()
}

func main() {
    fmt.Println("NetTrayGuard started")

    // 注册关机事件
    syscall.SetConsoleCtrlHandler(func(ctrlType uint32) bool {
        switch ctrlType {
        case syscall.CTRL_SHUTDOWN_EVENT,
            syscall.CTRL_LOGOFF_EVENT,
            syscall.CTRL_CLOSE_EVENT:
            fmt.Println("Shutdown detected → cleaning up")
            disableSystemProxy()
            killMihomo()
            return true
        }
        return false
    }, true)

    // 守护进程永不退出
    for {
        time.Sleep(10 * time.Second)
    }
}
