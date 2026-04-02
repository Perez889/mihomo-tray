package main

import (
 _ "embed"
 "os"
 "os/exec"
 "path/filepath"
 "runtime"

 "github.com/getlantern/systray"
)

var mihomoCmd *exec.Cmd

//go:embed clash.ico
var trayIcon []byte

func main() {
 systray.Run(onReady, onExit)
}

func onReady() {
 if len(trayIcon) > 0 {
  systray.SetTemplateIcon(trayIcon, trayIcon)
 }

 // systray.SetTitle("Mihomo")
 systray.SetTooltip("Mihomo Proxy\n右键打开菜单")

 // 添加菜单项
 mStart := systray.AddMenuItem("启动", "启动 Mihomo")
 mOpen := systray.AddMenuItem("面板", "打开 Zashboard 面板")
 mQuit := systray.AddMenuItem("退出", "退出程序并关闭 mihomo")

 // 菜单点击处理
 go func() {
  for {
   select {
   case <-mStart.ClickedCh:
    startMihomo()
   case <-mOpen.ClickedCh:
    openDashboard()
   case <-mQuit.ClickedCh:
    systray.Quit()
    return
   }
  }
 }()
}

func startMihomo() {
 if mihomoCmd != nil && mihomoCmd.Process != nil {
  // 如果已经在运行，就不再重复启动
  return
 }

 baseDir := appDir()
 cmd := exec.Command(filepath.Join(baseDir, "mihomo"), "-f", filepath.Join(baseDir, "config.yaml"))
 if err := cmd.Start(); err == nil {
  mihomoCmd = cmd
 }
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
  mihomoCmd.Process.Kill()
  mihomoCmd.Wait()
 }
}

func init() {
 // 程序启动时自动启动 mihomo
 startMihomo()
}

func appDir() string {
 exePath, err := os.Executable()
 if err != nil {
  return "."
 }

 return filepath.Dir(exePath)
}
