package main

import (
    _ "embed"
    "bytes"
    "fmt"
    "net/http"
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

// 你的 secret（来自 config.yaml）
const mihomoSecret = "password"

func main() {
    systray.Run(onReady, onExit)
}

func onReady() {
    if runtime.GOOS == "windows" {
        systray.SetIcon(trayIcon)
    } else {
        systray.SetTemplateIcon(trayIcon, trayIcon)
    }

    systray.SetTooltip("Mihomo Proxy\n右键打开菜单")

    mStart := systray.AddMenuItem("启动", "启动 Mihomo")
    mOpen := systray.AddMenuItem("面板", "打开 Zashboard 面板")

    mSysProxy := systray.AddMenuItemCheckbox("代理", "开关系统代理", false)
    mTun := systray.AddMenuItemCheckbox("TUN", "开关 TUN", false)

    mQuit := systray.AddMenuItem("退出", "退出程序并关闭 mihomo")

    go func() {
        for {
            select {
            case <-mStart.ClickedCh:
                startMihomo()

            case <-mOpen.ClickedCh:
                openDashboard()

            case <-mSysProxy.ClickedCh:
                if mSysProxy.Checked() {
                    mSysProxy.Uncheck()
                    toggleSystemProxy(false)
                } else {
                    mSysProxy.Check()
                    toggleSystemProxy(true)
                }

            case <-mTun.ClickedCh:
                if mTun.Checked() {
                    mTun.Uncheck()
                    toggleTun(false)
                } else {
                    mTun.Check()
                    toggleTun(true)
                }

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

func onExit() {
    if mihomoCmd != nil && mihomoCmd.Process != nil {
        fmt.Println("关闭 mihomo...")
        mihomoCmd.Process.Kill()
        mihomoCmd.Wait()
    }
}

// ---------------------------
// 🔥 Mihomo API 控制部分（带 Authorization）
// ---------------------------

func apiPost(url string) {
    req, _ := http.NewRequest("POST", url, bytes.NewBuffer([]byte("{}")))
    req.Header.Set("Authorization", "Bearer "+mihomoSecret)
    req.Header.Set("Content-Type", "application/json")

    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        fmt.Println("API 调用失败:", err)
        return
    }
    fmt.Println("API 返回:", resp.Status)
}

func toggleSystemProxy(enable bool) {
    url := "http://127.0.0.1:9090/system/"
    if enable {
        url += "enable"
    } else {
        url += "disable"
    }
    apiPost(url)
}

func toggleTun(enable bool) {
    url := "http://127.0.0.1:9090/tun/"
    if enable {
        url += "enable"
    } else {
        url += "disable"
    }
    apiPost(url)
}

// ---------------------------

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
