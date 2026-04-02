package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/getlantern/systray"
)

func main() {
	systray.Run(onReady, onExit)
}

func onReady() {
	// 尝试加载图标（优先 ico，其次 png）
	iconData, err := os.ReadFile("clash.ico")
	if err != nil || len(iconData) == 0 {
		iconData, err = os.ReadFile("clash.png")
	}
	if err != nil || len(iconData) == 0 {
		// 如果都没找到，用一个简单默认图标（蓝色方块）
		fmt.Println("警告：未找到 clash.ico 或 clash.png，使用默认图标")
		iconData = []byte{} // systray 会用默认图标
	}

	systray.SetIcon(iconData)
	systray.SetTitle("Mihomo")
	systray.SetTooltip("Mihomo Proxy")

	mOpen := systray.AddMenuItem("面板", "打开 Mihomo 面板")
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
	// 自动启动 mihomo（隐藏窗口）
	cmd := exec.Command("mihomo.exe", "-f", "config.yaml")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Start()
}
