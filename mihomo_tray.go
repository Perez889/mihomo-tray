package main

import (
	"bytes"
	"encoding/json"
	_ "embed"
	"fmt"
	"net"
	"net/http"
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

// 全局状态
var isSystemProxyEnabled bool = false
var isTUNEnabled bool = true // 默认与你的 config 一致

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

	// 原有菜单
	mRestart := systray.AddMenuItem("重启 Mihomo", "重启核心")
	mOpen := systray.AddMenuItem("打开面板", "打开 Zashboard")
	systray.AddSeparator()

	// 新增功能菜单
	mSystemProxy := systray.AddMenuItem("系统代理", "开启/关闭系统代理 (指向 mixed-port 1081)")
	mTUN := systray.AddMenuItem("TUN 虚拟网卡", "开启/关闭 TUN 模式")
	systray.AddSeparator()

	mQuit := systray.AddMenuItem("退出", "退出程序并关闭 mihomo")

	// 状态栏（只读显示）
	mStatus := systray.AddMenuItem(fmt.Sprintf("状态: TUN %s | 系统代理 %s", getTUNStatus(), getProxyStatus()), "")
	mStatus.Disable()

	go func() {
		for {
			select {
			case <-mRestart.ClickedCh:
				restartMihomo()
			case <-mOpen.ClickedCh:
				openDashboard()
			case <-mSystemProxy.ClickedCh:
				toggleSystemProxy(mSystemProxy, mStatus)
			case <-mTUN.ClickedCh:
				toggleTUNMode(mTUN, mStatus)
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()

	// 启动 Mihomo
	go startMihomo()
}

func getTUNStatus() string {
	if isTUNEnabled {
		return "已开启"
	}
	return "已关闭"
}

func getProxyStatus() string {
	if isSystemProxyEnabled {
		return "已开启"
	}
	return "已关闭"
}

// ==================== 系统代理开关 ====================
func toggleSystemProxy(menu *systray.MenuItem, status *systray.MenuItem) {
	isSystemProxyEnabled = !isSystemProxyEnabled

	proxyURL := "127.0.0.1:1081" // 与你的 mixed-port 一致

	if isSystemProxyEnabled {
		enableSystemProxy(proxyURL)
		menu.SetTitle("系统代理 (已开启)")
	} else {
		disableSystemProxy()
		menu.SetTitle("系统代理 (已关闭)")
	}

	// 更新状态栏
	status.SetTitle(fmt.Sprintf("状态: TUN %s | 系统代理 %s", getTUNStatus(), getProxyStatus()))
}

func enableSystemProxy(proxyAddr string) {
	fmt.Println("正在开启系统代理 →", proxyAddr)

	switch runtime.GOOS {
	case "windows":
		// Windows 使用注册表 + InternetSetOption（简单有效）
		cmd := exec.Command("powershell", "-Command",
			`Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyServer -Value "`+proxyAddr+`"; `+
				`Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyEnable -Value 1`)
		cmd.Run()
	case "darwin":
		// macOS（假设主要网卡是 Wi-Fi，可自行改成 Ethernet）
		exec.Command("networksetup", "-setwebproxy", "Wi-Fi", "127.0.0.1", "1081").Start()
		exec.Command("networksetup", "-setsecurewebproxy", "Wi-Fi", "127.0.0.1", "1081").Start()
		exec.Command("networksetup", "-setsocksfirewallproxy", "Wi-Fi", "127.0.0.1", "1081").Start()
	default:
		// Linux（GNOME 示例）
		exec.Command("gsettings", "set", "org.gnome.system.proxy", "mode", "manual").Run()
		exec.Command("gsettings", "set", "org.gnome.system.proxy.http", "host", "127.0.0.1").Run()
		exec.Command("gsettings", "set", "org.gnome.system.proxy.http", "port", "1081").Run()
	}
}

func disableSystemProxy() {
	fmt.Println("正在关闭系统代理")

	switch runtime.GOOS {
	case "windows":
		cmd := exec.Command("powershell", "-Command",
			`Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyEnable -Value 0`)
		cmd.Run()
	case "darwin":
		exec.Command("networksetup", "-setwebproxystate", "Wi-Fi", "off").Start()
		exec.Command("networksetup", "-setsecurewebproxystate", "Wi-Fi", "off").Start()
		exec.Command("networksetup", "-setsocksfirewallproxystate", "Wi-Fi", "off").Start()
	default:
		exec.Command("gsettings", "set", "org.gnome.system.proxy", "mode", "none").Run()
	}
}

// ==================== TUN 模式开关 ====================
func toggleTUNMode(menu *systray.MenuItem, status *systray.MenuItem) {
	isTUNEnabled = !isTUNEnabled

	payload := map[string]interface{}{
		"tun": map[string]interface{}{
			"enable": isTUNEnabled,
		},
	}

	jsonData, _ := json.Marshal(payload)

	// 调用 mihomo API（带 secret）
	req, _ := http.NewRequest("PATCH", "http://127.0.0.1:9090/configs?force=true", bytes.NewReader(jsonData))
	req.Header.Set("Authorization", "Bearer password") // 你的 secret

	client := &http.Client{Timeout: 5 * time.Second}
	_, err := client.Do(req)

	if err != nil {
		fmt.Println("TUN 切换失败（API 调用错误）:", err)
		isTUNEnabled = !isTUNEnabled // 回滚状态
		return
	}

	if isTUNEnabled {
		menu.SetTitle("TUN 虚拟网卡 (已开启)")
		fmt.Println("TUN 模式已开启")
	} else {
		menu.SetTitle("TUN 虚拟网卡 (已关闭)")
		fmt.Println("TUN 模式已关闭")
	}

	// TUN 切换通常需要重启核心才能彻底生效
	restartMihomo()

	// 更新状态栏
	status.SetTitle(fmt.Sprintf("状态: TUN %s | 系统代理 %s", getTUNStatus(), getProxyStatus()))
}

// ==================== 下面是你的原有函数（基本不变）===================

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
		_ = mihomoCmd.Process.Kill()
		_, _ = mihomoCmd.Process.Wait()
		mihomoCmd = nil
	}
	waitForRelease()
	startMihomoForce()
}

func waitForRelease() {
	time.Sleep(2 * time.Second)
	for i := 0; i < 8; i++ { // 稍微延长等待
		if !isPortOpen("127.0.0.1:9090") && !isPortOpen("127.0.0.1:1081") {
			break
		}
		time.Sleep(600 * time.Millisecond)
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
		_ = mihomoCmd.Process.Kill()
		_, _ = mihomoCmd.Process.Wait()
	}
	// 退出时自动关闭系统代理（推荐）
	disableSystemProxy()
}

func appDir() string {
	exePath, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exePath)
}
