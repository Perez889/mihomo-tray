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
	"gopkg.in/yaml.v3" // go get gopkg.in/yaml.v3
)

//go:embed clash.ico
var trayIcon []byte

var mihomoCmd *exec.Cmd

// 全局状态
var isSystemProxyEnabled bool = false
var currentMixedPort string = "1081"     // 从 config.yaml 自动读取
var controllerPort string = "9090"       // 从 config.yaml 自动读取
var dashboardURL string = "http://127.0.0.1:9090/ui/zashboard"

func main() {
	loadConfigFromYAML() // 启动时自动读取端口
	systray.Run(onReady, onExit)
}

// ==================== 自动读取 config.yaml ====================
func loadConfigFromYAML() {
	baseDir := appDir()
	configPath := filepath.Join(baseDir, "config.yaml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Println("警告：无法读取 config.yaml，使用默认端口")
		return
	}

	var cfg map[string]interface{}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		fmt.Println("警告：解析 config.yaml 失败")
		return
	}

	// 读取 mixed-port（系统代理端口）
	if p, ok := cfg["mixed-port"]; ok {
		if port, ok := p.(int); ok {
			currentMixedPort = strconv.Itoa(port)
		} else if portStr, ok := p.(string); ok {
			currentMixedPort = strings.TrimSpace(portStr)
		}
	}

	// 读取 external-controller（面板端口）
	if ctrl, ok := cfg["external-controller"]; ok {
		if ctrlStr, ok := ctrl.(string); ok {
			// 支持 "127.0.0.1:9090" 或 "9090" 格式
			parts := strings.Split(ctrlStr, ":")
			if len(parts) > 1 {
				controllerPort = parts[len(parts)-1]
			} else {
				controllerPort = ctrlStr
			}
		}
	}

	dashboardURL = "http://127.0.0.1:" + controllerPort + "/ui/zashboard"
	fmt.Printf("自动识别 → 系统代理端口: %s | 面板端口: %s\n", currentMixedPort, controllerPort)
}

func onReady() {
	if runtime.GOOS == "windows" {
		systray.SetIcon(trayIcon)
	} else {
		systray.SetTemplateIcon(trayIcon, trayIcon)
	}
	systray.SetTooltip("Mihomo Proxy\n托盘管理小工具")

	mRestart := systray.AddMenuItem("重启内核", "重启 Mihomo")
	mOpen := systray.AddMenuItem("面板管理", "打开 Zashboard 面板")
	systray.AddSeparator()

	// Checkbox 系统代理（勾选启用）
	mProxy := systray.AddMenuItemCheckbox(
		fmt.Sprintf("系统代理 (%s)", currentMixedPort),
		"开启/关闭系统代理",
		false,
	)

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出程序", "退出程序并关闭 mihomo")

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
	title := fmt.Sprintf("系统代理 (%s)", currentMixedPort)
	if isSystemProxyEnabled {
		m.Check()
		m.SetTitle(title + " 已开启")
	} else {
		m.Uncheck()
		m.SetTitle(title + " 已关闭")
	}
}

// ==================== 系统代理开关 ====================
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
		exec.Command("networksetup", "-setwebproxy", "Wi-Fi", "127.0.0.1", currentMixedPort).Start()
		exec.Command("networksetup", "-setsecurewebproxy", "Wi-Fi", "127.0.0.1", currentMixedPort).Start()
		exec.Command("networksetup", "-setsocksfirewallproxy", "Wi-Fi", "127.0.0.1", currentMixedPort).Start()
	default:
		exec.Command("gsettings", "set", "org.gnome.system.proxy", "mode", "manual").Run()
		exec.Command("gsettings", "set", "org.gnome.system.proxy.http", "host", "127.0.0.1").Run()
		exec.Command("gsettings", "set", "org.gnome.system.proxy.http", "port", currentMixedPort).Run()
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

// ==================== 下面是你的原功能（保持不变）===================
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
	for i := 0; i < 8; i++ {
		if !isPortOpen("127.0.0.1:"+controllerPort) && !isPortOpen("127.0.0.1:"+currentMixedPort) {
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
		cmd = exec.Command("open", dashboardURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", dashboardURL)
	default:
		cmd = exec.Command("xdg-open", dashboardURL)
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
	disableSystemProxy() // 退出或关机时自动关闭系统代理
}

func appDir() string {
	exePath, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exePath)
}
