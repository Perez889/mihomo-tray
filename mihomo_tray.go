package main

import (
	_ "embed"
	"fmt"
	"net"
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

// 全局状态：false = 关闭，true = 开启
var isSystemProxyEnabled bool = false

const (
	MIXED_PORT   = "127.0.0.1:1081"
	CONTROLLER   = "127.0.0.1:9090"
	DASHBOARD_URL = "http://127.0.0.1:9090/ui/zashboard"
)

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

	// 菜单
	mRestart := systray.AddMenuItem("重启", "重启 Mihomo 核心")
	mOpen := systray.AddMenuItem("面板", "打开 Zashboard 面板")
	systray.AddSeparator()

	// 系统代理 - 使用 Checkbox（勾选即开启）
	mProxy := systray.AddMenuItemCheckbox("代理", "开启/关闭系统代理 (1081)", false)

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", "退出程序并关闭 mihomo")

	// 初始化勾选状态
	updateProxyMenu(mProxy)

	go func() {
		for {
			select {
			case <-mRestart.ClickedCh:
				restartMihomo()
			case <-mOpen.ClickedCh:
				openDashboard()
			case <-mProxy.ClickedCh:
				toggleSystemProxy(mProxy)
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()

	// 启动 Mihomo
	go startMihomo()
}

// 更新 Checkbox 状态
func updateProxyMenu(m *systray.MenuItem) {
	if isSystemProxyEnabled {
		m.Check()
		m.SetTitle("系统代理 (已开启)")
	} else {
		m.Uncheck()
		m.SetTitle("系统代理 (已关闭)")
	}
}

// ==================== 系统代理开关（Checkbox + 无闪窗）===================
func toggleSystemProxy(m *systray.MenuItem) {
	isSystemProxyEnabled = !isSystemProxyEnabled

	if isSystemProxyEnabled {
		enableSystemProxy()
	} else {
		disableSystemProxy()
	}

	updateProxyMenu(m)
}

func enableSystemProxy() {
	fmt.Println("开启系统代理 →", MIXED_PORT)

	switch runtime.GOOS {
	case "windows":
		cmd := exec.Command("powershell", "-Command",
			`Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyServer -Value "`+MIXED_PORT+`"; `+
				`Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyEnable -Value 1`)
		hideWindow(cmd)
		_ = cmd.Run()
	case "darwin":
		exec.Command("networksetup", "-setwebproxy", "Wi-Fi", "127.0.0.1", "1081").Start()
		exec.Command("networksetup", "-setsecurewebproxy", "Wi-Fi", "127.0.0.1", "1081").Start()
		exec.Command("networksetup", "-setsocksfirewallproxy", "Wi-Fi", "127.0.0.1", "1081").Start()
	default:
		// Linux GNOME 示例
		exec.Command("gsettings", "set", "org.gnome.system.proxy", "mode", "manual").Run()
		exec.Command("gsettings", "set", "org.gnome.system.proxy.http", "host", "127.0.0.1").Run()
		exec.Command("gsettings", "set", "org.gnome.system.proxy.http", "port", "1081").Run()
	}
}

func disableSystemProxy() {
	fmt.Println("关闭系统代理")

	switch runtime.GOOS {
	case "windows":
		cmd := exec.Command("powershell", "-Command",
			`Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyEnable -Value 0`)
		hideWindow(cmd)
		_ = cmd.Run()
	case "darwin":
		exec.Command("networksetup", "-setwebproxystate", "Wi-Fi", "off").Start()
		exec.Command("networksetup", "-setsecurewebproxystate", "Wi-Fi", "off").Start()
		exec.Command("networksetup", "-setsocksfirewallproxystate", "Wi-Fi", "off").Start()
	default:
		exec.Command("gsettings", "set", "org.gnome.system.proxy", "mode", "none").Run()
	}
}

// 隐藏 Windows 子命令窗口（解决闪屏关键）
func hideWindow(cmd *exec.Cmd) {
	if runtime.GOOS == "windows" && cmd != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:    true,
			CreationFlags: 0x08000000, // CREATE_NO_WINDOW
		}
	}
}

// ==================== Mihomo 核心控制（已优化）===================
func startMihomo() {
	if isRunning(mihomoCmd) {
		fmt.Println("mihomo 已在运行")
		return
	}
	startMihomoForce()
}

func startMihomoForce() {
	baseDir := appDir()
	exeName := "mihomo"
	if runtime.GOOS == "windows" {
		exeName = "mihomo.exe"
	}
	exePath := filepath.Join(baseDir, exeName)

	cmd := exec.Command(exePath, "-d", ".")
	cmd.Dir = baseDir
	hideWindow(cmd) // 隐藏 mihomo 自身窗口

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
		_ = mihomoCmd.Process.Kill()
		_, _ = mihomoCmd.Process.Wait()
		mihomoCmd = nil
	}
	waitForRelease()
	startMihomoForce()
}

func waitForRelease() {
	time.Sleep(2 * time.Second)
	for i := 0; i < 10; i++ {
		if !isPortOpen(CONTROLLER) && !isPortOpen(MIXED_PORT) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func isPortOpen(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func isRunning(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	err := cmd.Process.Signal(syscall.Signal(0))
	return err == nil
}

func openDashboard() {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", DASHBOARD_URL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", DASHBOARD_URL)
	default:
		cmd = exec.Command("xdg-open", DASHBOARD_URL)
	}
	hideWindow(cmd)
	cmd.Start()
}

func onExit() {
	if mihomoCmd != nil && mihomoCmd.Process != nil {
		fmt.Println("关闭 mihomo...")
		_ = mihomoCmd.Process.Kill()
		_, _ = mihomoCmd.Process.Wait()
	}
	disableSystemProxy() // 退出时自动关闭系统代理
}

func appDir() string {
	exePath, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exePath)
}
