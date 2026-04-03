package main

import (
    _ "embed"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "runtime"
    "syscall"
    "time"

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

    systray.SetTooltip("Mihomo Proxy\n托盘管理小工具")

    mRestart := systray.AddMenuItem("重启", "重启 Mihomo")
    mOpen := systray.AddMenuItem("面板", "打开 Zashboard 面板")
    mQuit := systray.AddMenuItem("退出", "退出程序并关闭 mihomo")

    go func() {
        for {
            select {
            case <-mRestart.ClickedCh:
                restartMihomo()
            case <-mOpen.ClickedCh:
                openDashboard()
            case <-mQuit.ClickedCh:
                systray.Quit()
                return
            }
        }
    }()

    // ⭐ 程序启动时自动启动 Mihomo
    go startMihomo()
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

    // ⭐ 等效 bat：cd /d %~dp0 && mihomo.exe -d .
    cmd := exec.Command(exePath, "-d", ".")
    cmd.Dir = baseDir

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

func restartMihomo() {
    fmt.Println("正在重启 mihomo...")

    if mihomoCmd != nil && mihomoCmd.Process != nil {
        mihomoCmd.Process.Kill()
        mihomoCmd.Wait()

        // ⭐ 关键：清空句柄，否则 startMihomo() 会误判“已在运行”
        mihomoCmd = nil
    }

    // ⭐ 给系统一点时间释放端口（非常重要）
    time.Sleep(500 * time.Millisecond)

    startMihomo()
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

func onExit() {
    if mihomoCmd != nil && mihomoCmd.Process != nil {
        fmt.Println("关闭 mihomo...")
        mihomoCmd.Process.Kill()
        mihomoCmd.Wait()
    }
}

func appDir() string {
    exePath, err := os.Executable()
    if err != nil {
        return "."
    }
    return filepath.Dir(exePath)
}
