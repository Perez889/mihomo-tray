package main

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/getlantern/systray"
)

func main() {
	systray.Run(onReady, onExit)
}

func onReady() {
	iconBytes, _ := os.ReadFile("clash.ico") // 推荐用 clash.ico
	if len(iconBytes) == 0 {
		iconBytes, _ = os.ReadFile("clash.png")
	}
	systray.SetIcon(iconBytes)
	systray.SetTitle("Mihomo")
	systray.SetTooltip("Mihomo Proxy")

	mOpen := systray.AddMenuItem("面板", "")
	mQuit := systray.AddMenuItem("退出", "")

	systray.SetOnClick(openDashboard)

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				openDashboard()
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

func openDashboard() {
	exec.Command("rundll32", "url.dll,FileProtocolHandler", "http://127.0.0.1:9090/ui/zashboard").Start()
}

func onExit() {
	exec.Command("taskkill", "/f", "/im", "mihomo.exe").Run()
}

func init() {
	// 自动启动 mihomo（无窗口）
	cmd := exec.Command("mihomo.exe", "-f", "config.yaml")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Start()
}