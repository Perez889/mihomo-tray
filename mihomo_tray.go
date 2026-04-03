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
	// 托盘图标
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

	// 启动
	go startMihomo()
}

//
// ✅ 启动（带运行检测）
//
func startMihomo() {
	if isRunning(mihomoCmd) {
		fmt.Println("mihomo 已在运行")
		return
	}
	startMihomoForce()
}

//
// ✅ 强制启动（重启专用）
//
func startMihomoForce() {
	baseDir := appDir()

	exeName := "mihomo"
	if runtime.GOOS == "windows" {
		exeName = "mihomo.exe"
	}

	exePath := filepath.Join(baseDir, exeName)

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

//
// ✅ 重启（已修复 TUN 问题）
//
func restartMihomo() {
	fmt.Println("正在重启 mihomo...")

	if mihomoCmd != nil && mihomoCmd.Process != nil {
		_ = mihomoCmd.Process.Kill()
		_, _ = mihomoCmd.Process.Wait()
		mihomoCmd = nil
	}

	// ⭐ TUN 模式必须延迟（关键）
	waitForRelease()

	startMihomoForce()
}

//
// ✅ 等待端口 / TUN 释放（核心优化）
//
func waitForRelease() {
	// 最少等待 2 秒（TUN 必须）
	time.Sleep(2 * time.Second)

	// 再额外检测几轮（更稳）
	for i := 0; i < 5; i++ {
		if !isPortInUse("127.0.0.1:9090") {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
}

//
// ✅ 检测端口是否被占用
//
func isPortInUse(addr string) bool {
	conn, err := exec.Command("cmd", "/c", "netstat -ano | findstr "+addr).Output()
	return err == nil && len(conn) > 0
}

//
// ✅ 判断进程是否真的在运行
//
func isRunning(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	err := cmd.Process.Signal(syscall.Signal(0))
	return err == nil
}

//
// ✅ 打开面板
//
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

//
// ✅ 退出清理
//
func onExit() {
	if mihomoCmd != nil && mihomoCmd.Process != nil {
		fmt.Println("关闭 mihomo...")
		_ = mihomoCmd.Process.Kill()
		_, _ = mihomoCmd.Process.Wait()
	}
}

//
// ✅ 获取程序目录（便携核心）
//
func appDir() string {
	exePath, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exePath)
}
