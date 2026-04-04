package main

import (
	_ "embed"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/getlantern/systray"
	"gopkg.in/yaml.v3" // 需要执行: go get gopkg.in/yaml.v3
)

//go:embed clash.ico
var trayIcon []byte

var mihomoCmd *exec.Cmd

// 全局状态
var isSystemProxyEnabled bool = false
var currentMixedPort string = "1081" // 默认值，会被 config.yaml 覆盖

const (
	CONTROLLER   = "127.0.0.1:9090"
	DASHBOARD_URL = "http://127.0.0.1:9090/ui/zashboard"
)

func main() {
	loadMixedPortFromConfig() // 启动时读取 config.yaml 中的 mixed-port
	systray.Run(onReady, onExit)
}

// 从 config.yaml 读取 mixed-port（优先使用 mixed-port，其次 socks-port / port）
func loadMixedPortFromConfig() {
	baseDir := appDir()
	configPath := filepath.Join(baseDir, "config.yaml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Println("警告：无法读取 config.yaml，使用默认端口 1081")
		return
	}

	var config map[string]interface{}
	if err := yaml.Unmarshal(data, &config); err != nil {
		fmt.Println("警告：解析 config.yaml 失败，使用默认端口 1081")
		return
	}

	// 优先级：mixed-port > socks-port > port
	if port, ok := config["mixed-port"]; ok {
		if p, ok := port.(int); ok {
			currentMixedPort = strconv.Itoa(p)
		} else if p, ok := port.(string); ok {
			currentMixedPort = strings.TrimSpace(p)
		}
	} else if port, ok := config["socks-port"]; ok {
		if p, ok := port.(int); ok {
			currentMixedPort = strconv.Itoa(p)
		}
	} else if port, ok := config["port"]; ok {
		if p, ok := port.(int); ok {
			currentMixedPort = strconv.Itoa(p)
		}
	}

	fmt.Println("当前系统代理端口（来自 config.yaml）:", currentMixedPort)
}

func onReady() {
	if runtime.GOOS == "windows" {
		systray.SetIcon(trayIcon)
	} else {
		systray.SetTemplateIcon(trayIcon, trayIcon)
	}
	systray.SetTooltip("Mihomo Proxy\n托盘管理小工具")

	mRestart := systray.AddMenuItem("重启", "重启 Mihomo 核心")
	mOpen := systray.AddMenuItem("面板", "打开 Zashboard 面板")
	systray.AddSeparator()

	// Checkbox 菜单，动态显示端口
	mProxy := systray.AddMenuItemCheckbox("代理", fmt.Sprintf("开启/关闭系统代理 (%s)", currentMixedPort), false)
	systray.AddSeparator()

	mQuit := systray.AddMenuItem("退出", "退出程序并关闭 mihomo")

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

	go startMihomo()
}

// 更新 Checkbox 状态和标题
func updateProxyMenu(m *systray.MenuItem) {
	title := fmt.Sprintf("代理 (%s)", currentMixedPort)
	if isSystemProxyEnabled {
		m.Check()
		m.SetTitle(title + " 已开启")
	} else {
		m.Uncheck()
		m.SetTitle(title + " 已关闭")
	}
}

// ==================== 系统代理开关（使用实际端口）===================
func toggleSystemProxy(m *systray.MenuItem) {
	isSystemProxyEnabled = !isSystemProxyEnabled

	proxyAddr := "127.0.0.1:" + currentMixedPort

	if isSystemProxyEnabled {
		enableSystemProxy(proxyAddr)
	} else {
		disableSystemProxy()
	}
	updateProxyMenu(m)
}

func enableSystemProxy(proxyAddr string) {
	fmt.Println("开启系统代理 →", proxyAddr)

	switch runtime.GOOS {
	case "windows":
		cmd := exec.Command("powershell", "-Command",
			`Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyServer -Value "`+proxyAddr+`"; `+
				`Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyEnable -Value 1`)
		hideWindow(cmd)
		_ = cmd.Run()
	case "darwin":
		port := currentMixedPort
		exec.Command("networksetup", "-setwebproxy", "Wi-Fi", "127.0.0.1", port).Start()
		exec.Command("networksetup", "-setsecurewebproxy", "Wi-Fi", "127.0.0.1", port).Start()
		exec.Command("networksetup", "-setsocksfirewallproxy", "Wi-Fi", "127.0.0.1", port).Start()
	default:
		port := currentMixedPort
		exec.Command("gsettings", "set", "org.gnome.system.proxy", "mode", "manual").Run()
		exec.Command("gsettings", "set", "org.gnome.system.proxy.http", "host", "127.0.0.1").Run()
		exec.Command("gsettings", "set", "org.gnome.system.proxy.http", "port", port).Run()
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

func hideWindow(cmd *exec.Cmd) {
	if runtime.GOOS == "windows" && cmd != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:    true,
			CreationFlags: 0x08000000,
		}
	}
}

// ==================== 下面函数基本不变（已优化）===================
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
	hideWindow(cmd)

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
		if !isPortOpen(CONTROLLER) && !isPortOpen("127.0.0.1:"+currentMixedPort) {
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
	disableSystemProxy()
}

func appDir() string {
	exePath, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exePath)
}
