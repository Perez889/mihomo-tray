package main

import (
	"os/exec"
	"syscall"

	"github.com/getlantern/systray"
)

func main() {
	systray.Run(onReady, onExit)
}

func onReady() {
	// 使用系统默认图标（避免 clash.ico 加载失败导致托盘不出现）
	systray.SetTitle("Mihomo")
	systray.SetTooltip("Mihomo Proxy\n双击或右键打开菜单")

	mOpen := systray.AddMenuItem("面板", "打开 Zashboard 面板")
	mQuit := systray.AddMenuItem("退出", "退出程序并关闭 mihomo")

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
	// 启动 mihomo（隐藏窗口）
	cmd := exec.Command("mihomo.exe", "-f", "config.yaml")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Start()
}
