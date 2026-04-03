package main

import (
    _ "embed"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "runtime"
    "syscall"

    "github.com/getlantern/systray"
)

//go:embed clash.ico
var trayIcon []byte

var mihomoCmd *exec.Cmd

func main() {
    systray.Run(onReady, onExit)
}

func onReady() {
    // 设置托盘图标
    if runtime.GOOS == "windows" {
        systray.SetIcon(trayIcon)
    } else {
        systray.SetTemplateIcon(trayIcon, trayIcon)
    }

    systray.SetTooltip("Mihomo Proxy\n右键打开菜单")

    // 菜单
    mStart := systray.AddMenuItem("启动", "启动 Mihomo")
    mOpen := systray.AddMenuItem("面板", "打开 Zashboard 面板")
    mSysProxy := systray.AddMenuItem("代理", "启用/禁用系统代理")
    mTun := systray.AddMenuItem("TUN", "启用/禁用虚拟网卡")
    mQuit := systray.AddMenuItem("退出", "退出程序并关闭 mihomo")

    // 菜单事件处理
    go func() {
        for {
            select {
            case <-mStart.ClickedCh:
                startMihomo()
            case <-mOpen.ClickedCh:
                openDashboard()
            case <-mSysProxy.ClickedCh:
                toggleSystemProxy()
            case <-mTun.ClickedCh:
                toggleVirtualTun()
            case <-mQuit.ClickedCh:
                systray.Quit()
                return
            }
        }
    }()
}

func startMihomo() {
    if mihomoCmd != nil && mihomoCmd.Process != nil {
        fmt.Println("mihomo 已在运行")
        return
    }

    baseDir := appDir()
    exeName := "mihomo"
    if runtime.GOOS == "windows" {
        exeName = "mihomo.exe"
    }
    exePath := filepath.Join(baseDir, exeName)
    configPath := filepath.Join(baseDir, "config.yaml")

    cmd := exec.Command(exePath, "-f", configPath)

    // Windows 隐藏窗口
    if runtime.GOOS == "windows" {
        cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
    }

    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr

    if err := cmd.Start(); err != nil {
        fmt.Println("启动失败:", err)
        return
    }

    mihomoCmd = cmd
    fmt.Println("mihomo 启动成功")
}

func openDashboard() {
    url := "http://127.0.0.1:9090/ui/zashboard"
    switch runtime.GOOS {
    case "darwin":
        exec.Command("open", url).Start()
    case "windows":
        exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
    default:
        exec.Command("xdg-open", url).Start()
    }
}

func toggleSystemProxy() {
    if runtime.GOOS != "windows" {
        fmt.Println("系统代理功能仅在 Windows 支持")
        return
    }

    // 简单示例：设置系统 HTTP 代理到 127.0.0.1:7890
    cmd := exec.Command("netsh", "winhttp", "set", "proxy", "127.0.0.1:7890")
    if err := cmd.Run(); err != nil {
        fmt.Println("设置系统代理失败:", err)
        return
    }
    fmt.Println("系统代理已启用")
}

func toggleVirtualTun() {
    if runtime.GOOS != "windows" {
        fmt.Println("虚拟网卡功能仅在 Windows 支持")
        return
    }

    baseDir := appDir()
    exeName := "mihomo"
    if runtime.GOOS == "windows" {
        exeName = "mihomo.exe"
    }
    exePath := filepath.Join(baseDir, exeName)

    // 调用 mihomo tun 命令
    cmd := exec.Command(exePath, "tun")
    cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
    if err := cmd.Run(); err != nil {
        fmt.Println("虚拟网卡操作失败:", err)
        return
    }
    fmt.Println("虚拟网卡操作完成")
}

func onExit() {
    if mihomoCmd != nil && mihomoCmd.Process != nil {
        fmt.Println("关闭 mihomo...")
        mihomoCmd.Process.Kill()
        mihomoCmd.Wait()
    }
}

func init() {
    go startMihomo()
}

func appDir() string {
    exePath, err := os.Executable()
    if err != nil {
        return "."
    }
    return filepath.Dir(exePath)
}
