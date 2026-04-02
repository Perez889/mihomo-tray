package main

import (
	_ "embed"
	"fmt"
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
	// 设置托盘图标（关键修复）
	if len(trayIcon) > 0 {
		if runtime.GOOS == "windows" {
			systray.SetIcon(trayIcon)
		} else {
			systray.SetTemplateIcon(trayIcon, trayIcon)
		}
	}

	systray.SetTooltip("Mihomo Proxy\n右键打开菜单")

	// 菜单
	mStart := systray.AddMenuItem("启动", "启动 Mihomo")
	mOpen := systray.AddMenuItem("面板", "打开 Zashboard 面板")
	mQuit := systray.AddMenuItem("退出", "退出程序并关闭 mihomo")

	// 事件监听
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

// 启动 mihomo（已修复 Windows）
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

	fmt.Println("启动路径:", exePath)

	cmd := exec.Command(exePath, "-f", configPath)

	// 可选：输出日志到控制台（调试用）
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Start()
	if err != nil {
		fmt.Println("启动失败:", err)
		return
	}

	mihomoCmd = cmd
	fmt.Println("mihomo 启动成功")
}

// 打开面板
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

// 退出时关闭 mihomo
func onExit() {
	if mihomoCmd != nil && mihomoCmd.Process != nil {
		fmt.Println("关闭 mihomo...")
		mihomoCmd.Process.Kill()
		mihomoCmd.Wait()
	}
}

// 程序启动自动运行 mihomo
func init() {
	go startMihomo()
}

// 获取程序目录
func appDir() string {
	exePath, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exePath)
}
