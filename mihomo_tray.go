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
	// 读取图标（推荐 clash.ico，如果没有就用 clash.png）
	iconBytes, _ := os.ReadFile("clash.ico")
	if len(iconBytes) == 0 {
		iconBytes, _ = os.ReadFile("clash.png")
	}
	systray.SetIcon(iconBytes)
	systray.SetTitle("Mihomo")
	systray.SetTooltip("Mihomo Proxy")

	// 菜单
	mOpen := systray.AddMenuItem("面板", "打开 Mihomo 面板")
	mQuit := systray.AddMenuItem("退出", "退出程序并关闭 mihomo")

	// 点击菜单项
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
	// 退出时强制杀死 mihomo
	exec.Command("taskkill", "/f", "/im", "mihomo.exe").Run()
}

func init() {
	// 程序启动时自动运行 mihomo（完全隐藏窗口）
	cmd := exec.Command("mihomo.exe", "-f", "config.yaml")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Start()
}
