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

const (
	SECRET       = "password"                    // 与 config.yaml 中的 secret 一致
	MIXED_PORT   = "127.0.0.1:1081"
	CONTROLLER   = "127.0.0.1:9090"
	DASHBOARD_URL = "http://127.0.0.1:9090/ui/zashboard"
)

//go:embed clash.ico
var trayIcon []byte

var (
	mihomoCmd            *exec.Cmd
	isSystemProxyEnabled bool = false
	isTUNEnabled         bool = true // 默认与你的 config.yaml 一致

	httpClient = &http.Client{Timeout: 5 * time.Second}
)

func main() {
	systray.Run(onReady, onExit)
}

func onReady() {
	if runtime.GOOS == "windows" {
		systray.SetIcon(trayIcon)
	} else {
		systray.SetTemplateIcon(trayIcon, trayIcon)
	}
	systray.SetTooltip("Mihomo Proxy\n托盘管理小工具")

	// 菜单项（精简文字）
	mRestart := systray.AddMenuItem("重启", "重启 Mihomo 核心")
	mOpen := systray.AddMenuItem("面板", "打开 Zashboard 面板")
	systray.AddSeparator()

	mSystemProxy := systray.AddMenuItemCheckbox("代理", "开启/关闭系统代理", false)
	mTUN := systray.AddMenuItemCheckbox("TUN", "开启/关闭虚拟网卡", true)
	systray.AddSeparator()

	// 状态显示（上下两排）
	mTUNStatus := systray.AddMenuItem("TUN: 已开启", "")
	mProxyStatus := systray.AddMenuItem("代理: 已关闭", "")
	mTUNStatus.Disable()
	mProxyStatus.Disable()

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", "退出并关闭 Mihomo")

	// 初始化状态
	updateMenuState(mSystemProxy, mTUN, mTUNStatus, mProxyStatus)

	go func() {
		for {
			select {
			case <-mRestart.ClickedCh:
				restartMihomo()
				updateMenuState(mSystemProxy, mTUN, mTUNStatus, mProxyStatus)
			case <-mOpen.ClickedCh:
				openDashboard()
			case <-mSystemProxy.ClickedCh:
				toggleSystemProxy(mSystemProxy, mTUNStatus, mProxyStatus)
			case <-mTUN.ClickedCh:
				toggleTUNMode(mTUN, mTUNStatus, mProxyStatus)
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()

	go startMihomo()
}

// 更新勾选和状态文字
func updateMenuState(mProxy, mTUN *systray.MenuItem, mTUNStatus, mProxyStatus *systray.MenuItem) {
	if isSystemProxyEnabled {
		mProxy.Check()
		mProxyStatus.SetTitle("代理: 已开启")
	} else {
		mProxy.Uncheck()
		mProxyStatus.SetTitle("代理: 已关闭")
	}

	if isTUNEnabled {
		mTUN.Check()
		mTUNStatus.SetTitle("TUN: 已开启")
	} else {
		mTUN.Uncheck()
		mTUNStatus.SetTitle("TUN: 已关闭")
	}
}

// ==================== 系统代理开关 ====================
func toggleSystemProxy(mProxy *systray.MenuItem, mTUNStatus, mProxyStatus *systray.MenuItem) {
	isSystemProxyEnabled = !isSystemProxyEnabled

	if isSystemProxyEnabled {
		enableSystemProxy()
	} else {
		disableSystemProxy()
	}
	updateMenuState(mProxy, nil, mTUNStatus, mProxyStatus)
}

func enableSystemProxy() {
	fmt.Println("开启系统代理 →", MIXED_PORT)
	proxyAddr := MIXED_PORT

	switch runtime.GOOS {
	case "windows":
		cmd := exec.Command("powershell", "-Command",
			`Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyServer -Value "`+proxyAddr+`"; `+
				`Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyEnable -Value 1`)
		hideWindow(cmd)
		_ = cmd.Run()
	case "darwin":
		exec.Command("networksetup", "-setwebproxy", "Wi-Fi", "127.0.0.1", "1081").Start()
		exec.Command("networksetup", "-setsecurewebproxy", "Wi-Fi", "127.0.0.1", "1081").Start()
		exec.Command("networksetup", "-setsocksfirewallproxy", "Wi-Fi", "127.0.0.1", "1081").Start()
	default:
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

// ==================== TUN 模式开关 ====================
func toggleTUNMode(mTUN *systray.MenuItem, mTUNStatus, mProxyStatus *systray.MenuItem) {
	oldState := isTUNEnabled
	isTUNEnabled = !isTUNEnabled

	payload := map[string]interface{}{
		"tun": map[string]interface{}{"enable": isTUNEnabled},
	}
	jsonData, _ := json.Marshal(payload)

	req, _ := http.NewRequest("PATCH", "http://"+CONTROLLER+"/configs?force=true", bytes.NewReader(jsonData))
	req.Header.Set("Authorization", "Bearer "+SECRET)

	resp, err := httpClient.Do(req)
	if err != nil || (resp != nil && resp.StatusCode != 200) {
		fmt.Printf("TUN 切换失败: %v\n", err)
		isTUNEnabled = oldState
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		updateMenuState(nil, mTUN, mTUNStatus, mProxyStatus)
		return
	}
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}

	updateMenuState(nil, mTUN, mTUNStatus, mProxyStatus)
	fmt.Printf("TUN 模式已 %s\n", map[bool]string{true: "开启", false: "关闭"}[isTUNEnabled])

	restartMihomo()
}

// ==================== 隐藏窗口 ====================
func hideWindow(cmd *exec.Cmd) {
	if runtime.GOOS == "windows" && cmd != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:    true,
			CreationFlags: 0x08000000, // CREATE_NO_WINDOW
		}
	}
}

// ==================== Mihomo 核心控制 ====================
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
	time.Sleep(2500 * time.Millisecond)
	for i := 0; i < 12; i++ {
		if !isPortOpen(CONTROLLER) && !isPortOpen(MIXED_PORT) {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
}

func isPortOpen(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
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
