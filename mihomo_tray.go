package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/getlantern/systray"
	"golang.org/x/sys/windows"
	"gopkg.in/yaml.v3"
)

// ==================== 版本号 ====================
var Version = "dev"

//go:embed icon/logo.ico
var trayIcon []byte

// ==================== 单实例控制 ====================
var singleInstanceMutex windows.Handle

func ensureSingleInstance() {
	name, _ := windows.UTF16PtrFromString("Global\\MihomoTraySingleton")
	h, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		fmt.Println("创建互斥锁失败:", err)
		os.Exit(1)
	}
	singleInstanceMutex = h
	if windows.GetLastError() == windows.ERROR_ALREADY_EXISTS {
		fmt.Println("程序已经在运行")
		os.Exit(0)
	}
}

// ==================== 主应用结构体 ====================
type App struct {
	mihomoCmd            *exec.Cmd
	isSystemProxyEnabled bool
	isTUNEnabled         bool
	mixedPort            string
	controllerAddr       string
	secret               string
	dashboardURL         string
	currentMode          string // 当前代理模式（rule/global/direct）
}

func NewApp() *App {
	app := &App{
		isSystemProxyEnabled: false,
		isTUNEnabled:         false,
		mixedPort:            "1081",
		controllerAddr:       "127.0.0.1:9090",
		secret:               "",
		currentMode:          "",
	}
	app.loadConfig()
	return app
}

// ==================== 配置加载 ====================
func (a *App) loadConfig() {
	baseDir := a.appDir()
	configPath := filepath.Join(baseDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Println("警告：无法读取 config.yaml，使用默认值")
		a.buildDashboardURL()
		return
	}

	var cfg map[string]interface{}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		fmt.Println("警告：解析 config.yaml 失败，使用默认值")
		a.buildDashboardURL()
		return
	}

	// mixed-port
	if p, ok := cfg["mixed-port"]; ok {
		switch v := p.(type) {
		case int:
			a.mixedPort = strconv.Itoa(v)
		case float64:
			a.mixedPort = strconv.Itoa(int(v))
		case string:
			a.mixedPort = strings.TrimSpace(v)
		}
	}

	// external-controller
	if ctrl, ok := cfg["external-controller"]; ok {
		if ctrlStr, ok := ctrl.(string); ok && ctrlStr != "" {
			a.controllerAddr = strings.TrimSpace(ctrlStr)
		}
	}

	// secret
	if s, ok := cfg["secret"]; ok {
		if secretStr, ok := s.(string); ok {
			a.secret = strings.TrimSpace(secretStr)
		}
	}

	// ==================== 新增：读取 TUN 配置 ====================
	if tun, ok := cfg["tun"]; ok {
		if tunMap, ok := tun.(map[string]interface{}); ok {
			if enable, ok := tunMap["enable"].(bool); ok {
				a.isTUNEnabled = enable
				fmt.Printf("从 config.yaml 读取 TUN 配置 → enable: %v\n", enable)
			} else if enableStr, ok := tunMap["enable"].(string); ok {
				a.isTUNEnabled = strings.ToLower(strings.TrimSpace(enableStr)) == "true"
				fmt.Printf("从 config.yaml 读取 TUN 配置 → enable: %v (string)\n", a.isTUNEnabled)
			}
		}
	} else {
		fmt.Println("config.yaml 中未找到 tun 配置，默认 TUN: false")
		a.isTUNEnabled = false
	}
	// ========================================================

	a.buildDashboardURL()
	fmt.Printf("配置加载成功 → 端口: %s | 控制器: %s | Secret: %q | TUN: %v\n",
		a.mixedPort, a.controllerAddr, a.secret, a.isTUNEnabled)
}

func (a *App) buildDashboardURL() {
	if a.secret != "" {
		a.dashboardURL = fmt.Sprintf("http://%s/ui/zashboard?secret=%s", a.controllerAddr, a.secret)
	} else {
		a.dashboardURL = fmt.Sprintf("http://%s/ui/zashboard", a.controllerAddr)
	}
}

// ==================== UI 初始化 ====================
func (a *App) onReady() {
	if runtime.GOOS == "windows" {
		systray.SetIcon(trayIcon)
	} else {
		systray.SetTemplateIcon(trayIcon, trayIcon)
	}
	systray.SetTooltip("Mihomo Lite\n轻量托盘工具")

	mOpen := systray.AddMenuItem("打开面板", "打开 Zashboard")
	systray.AddSeparator()

	// 代理模式
	mMode := systray.AddMenuItem("出站模式", "切换代理模式")
	systray.AddSeparator()
	mRule := mMode.AddSubMenuItemCheckbox("规则模式", "Rule 模式", false)
	mGlobal := mMode.AddSubMenuItemCheckbox("全局模式", "Global 模式", false)
	mDirect := mMode.AddSubMenuItemCheckbox("直连模式", "Direct 模式", false)

	// 系统代理
	mProxy := systray.AddMenuItemCheckbox("系统代理", "点击切换系统代理开关", false)
	systray.AddSeparator()

	// TUN
	mTun := systray.AddMenuItemCheckbox("虚拟网卡 (TUN)", "切换 TUN 模式", false)
	systray.AddSeparator()

	mRestart := systray.AddMenuItem("重启内核", "重启 Mihomo")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出应用", "退出并关闭 mihomo")

	a.updateProxyMenu(mProxy)

	// 启动后同步状态（增加等待时间，让 mihomo 完全启动）
	go func() {
		time.Sleep(1800 * time.Millisecond) // 给 mihomo 启动留足够时间
		a.syncTunStateWithRetry(mTun, 15)
		a.syncModeStateWithRetry(mRule, mGlobal, mDirect, 12)
	}()

	go func() {
		for {
			select {
			case <-mRestart.ClickedCh:
				a.restartMihomo()
			case <-mOpen.ClickedCh:
				a.openDashboard()
			case <-mProxy.ClickedCh:
				a.toggleSystemProxy(mProxy)
			case <-mRule.ClickedCh:
				a.setMode("rule", mRule, mGlobal, mDirect)
			case <-mGlobal.ClickedCh:
				a.setMode("global", mRule, mGlobal, mDirect)
			case <-mDirect.ClickedCh:
				a.setMode("direct", mRule, mGlobal, mDirect)
			case <-mTun.ClickedCh:
				a.toggleTun(mTun)
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()

	go a.startMihomo()
}

// ==================== 代理模式相关（保持不变） ====================
func (a *App) updateModeUI(mode string, mRule, mGlobal, mDirect *systray.MenuItem) {
	mRule.Uncheck()
	mGlobal.Uncheck()
	mDirect.Uncheck()
	switch mode {
	case "rule":
		mRule.Check()
	case "global":
		mGlobal.Check()
	case "direct":
		mDirect.Check()
	}
}

func (a *App) syncModeStateWithRetry(mRule, mGlobal, mDirect *systray.MenuItem, maxRetries int) {
	for i := 0; i < maxRetries; i++ {
		if a.fetchAndUpdateModeState(mRule, mGlobal, mDirect) {
			return
		}
		time.Sleep(800 * time.Millisecond)
	}
	fmt.Println("警告：无法同步代理模式状态")
}

func (a *App) fetchAndUpdateModeState(mRule, mGlobal, mDirect *systray.MenuItem) bool {
	url := fmt.Sprintf("http://%s/configs", a.controllerAddr)
	req, _ := http.NewRequest("GET", url, nil)
	if a.secret != "" {
		req.Header.Set("Authorization", "Bearer "+a.secret)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return false
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return false
	}
	if mode, ok := data["mode"].(string); ok {
		a.currentMode = mode
		a.updateModeUI(mode, mRule, mGlobal, mDirect)
		return true
	}
	return false
}

func (a *App) setMode(mode string, mRule, mGlobal, mDirect *systray.MenuItem) {
	url := fmt.Sprintf("http://%s/configs", a.controllerAddr)
	body := fmt.Sprintf(`{"mode":"%s"}`, mode)
	req, err := http.NewRequest("PATCH", url, strings.NewReader(body))
	if err != nil {
		fmt.Println("设置模式失败:", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if a.secret != "" {
		req.Header.Set("Authorization", "Bearer "+a.secret)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("切换到 %s 模式失败\n", mode)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		fmt.Printf("切换到 %s 模式失败（HTTP %d）\n", mode, resp.StatusCode)
		return
	}
	a.currentMode = mode
	a.updateModeUI(mode, mRule, mGlobal, mDirect)
	fmt.Printf("已切换到 %s 模式\n", strings.ToUpper(mode))
}

// ==================== TUN 相关（已优化） ====================
func (a *App) syncTunStateWithRetry(m *systray.MenuItem, maxRetries int) {
	for i := 0; i < maxRetries; i++ {
		if a.fetchAndUpdateTunState(m) {
			return
		}
		time.Sleep(800 * time.Millisecond)
	}
	fmt.Println("警告：多次尝试后仍无法获取 TUN 状态")
}

func (a *App) fetchAndUpdateTunState(m *systray.MenuItem) bool {
	url := fmt.Sprintf("http://%s/configs", a.controllerAddr)
	req, _ := http.NewRequest("GET", url, nil)
	if a.secret != "" {
		req.Header.Set("Authorization", "Bearer "+a.secret)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return false
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return false
	}

	if tunCfg, ok := data["tun"].(map[string]interface{}); ok {
		if enabled, ok := tunCfg["enable"].(bool); ok {
			a.isTUNEnabled = enabled
			if enabled {
				m.Check()
			} else {
				m.Uncheck()
			}
			return true
		}
	}
	return false
}

func (a *App) toggleTun(m *systray.MenuItem) {
	newEnable := !a.isTUNEnabled

	fmt.Printf("尝试切换 TUN 模式 → %v\n", newEnable)

	if err := a.setTun(newEnable); err != nil {
		fmt.Printf("TUN PATCH 请求失败: %v\n", err)
		a.fetchAndUpdateTunState(m)
		return
	}

	// 关键修复：给 mihomo 时间应用 TUN 配置（创建/销毁虚拟网卡需要时间）
	time.Sleep(1200 * time.Millisecond)

	// 重新从内核获取真实状态
	if a.fetchAndUpdateTunState(m) {
		fmt.Printf("TUN 切换完成，当前实际状态: %v\n", a.isTUNEnabled)

		// TUN 开启时自动关闭系统代理（强烈推荐互斥）
		if a.isTUNEnabled && a.isSystemProxyEnabled {
			fmt.Println("TUN 已开启，自动关闭系统代理")
			a.disableSystemProxy()
			a.isSystemProxyEnabled = false
			// 注意：这里 mProxy 不在当前作用域，如果需要可把 mProxy 改为全局或通过其他方式更新
		}
	} else {
		fmt.Println("警告：TUN 切换后无法获取最新状态")
	}
}

func (a *App) setTun(enable bool) error {
	url := fmt.Sprintf("http://%s/configs", a.controllerAddr)
	body := fmt.Sprintf(`{"tun":{"enable":%v}}`, enable)
	req, err := http.NewRequest("PATCH", url, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.secret != "" {
		req.Header.Set("Authorization", "Bearer "+a.secret)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP 状态码: %d", resp.StatusCode)
	}
	return nil
}

// ==================== 系统代理（保持原样） ====================
func (a *App) updateProxyMenu(m *systray.MenuItem) {
	if a.isSystemProxyEnabled {
		m.Check()
	} else {
		m.Uncheck()
	}
}

func (a *App) toggleSystemProxy(m *systray.MenuItem) {
	// 可选增强：如果 TUN 已开启，阻止开启系统代理
	if !a.isSystemProxyEnabled && a.isTUNEnabled {
		fmt.Println("TUN 模式已开启，系统代理将被忽略")
		return
	}

	a.isSystemProxyEnabled = !a.isSystemProxyEnabled
	proxyAddr := "127.0.0.1:" + a.mixedPort

	if a.isSystemProxyEnabled {
		a.enableSystemProxy(proxyAddr)
		fmt.Println("系统代理已开启")
	} else {
		a.disableSystemProxy()
		fmt.Println("系统代理已关闭")
	}
	a.updateProxyMenu(m)
}

func (a *App) enableSystemProxy(proxyAddr string) {
	fmt.Println("开启系统代理 →", proxyAddr)
	if runtime.GOOS == "windows" {
		cmd := exec.Command("powershell", "-Command",
			`Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyServer -Value "`+proxyAddr+`"; `+
				`Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyEnable -Value 1`)
		a.hideWindow(cmd)
		_ = cmd.Run()
	}
}

func (a *App) disableSystemProxy() {
	fmt.Println("关闭系统代理")
	if runtime.GOOS == "windows" {
		cmd := exec.Command("powershell", "-Command",
			`Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyEnable -Value 0`)
		a.hideWindow(cmd)
		_ = cmd.Run()
	}
}

func (a *App) hideWindow(cmd *exec.Cmd) {
	if runtime.GOOS == "windows" && cmd != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:    true,
			CreationFlags: 0x08000000,
		}
	}
}

// ==================== Mihomo 核心控制（保持原样） ====================
func (a *App) startMihomo() {
	if a.isRunning(a.mihomoCmd) {
		fmt.Println("mihomo 已在运行")
		return
	}
	if a.isPortOpen("127.0.0.1:" + a.mixedPort) {
		fmt.Println("检测到端口已占用，跳过启动 mihomo")
		return
	}
	a.startMihomoForce()
}

func (a *App) startMihomoForce() {
	baseDir := a.appDir()
	exeName := "mihomo"
	if runtime.GOOS == "windows" {
		exeName = "mihomo.exe"
	}
	exePath := filepath.Join(baseDir, exeName)
	cmd := exec.Command(exePath, "-d", ".")
	cmd.Dir = baseDir
	a.hideWindow(cmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Println("启动失败:", err)
		return
	}
	a.mihomoCmd = cmd
	fmt.Println("mihomo 启动成功")
}

func (a *App) restartMihomo() {
	fmt.Println("正在重启 mihomo...")
	if a.mihomoCmd != nil && a.mihomoCmd.Process != nil {
		_ = a.mihomoCmd.Process.Kill()
		_, _ = a.mihomoCmd.Process.Wait()
		a.mihomoCmd = nil
	}
	a.waitForRelease()
	a.startMihomoForce()
}

func (a *App) waitForRelease() {
	time.Sleep(2 * time.Second)
	for i := 0; i < 8; i++ {
		if !a.isPortOpen(a.controllerAddr) && !a.isPortOpen("127.0.0.1:"+a.mixedPort) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (a *App) isPortOpen(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func (a *App) isRunning(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	err := cmd.Process.Signal(syscall.Signal(0))
	return err == nil
}

func (a *App) openDashboard() {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", a.dashboardURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", a.dashboardURL)
	default:
		cmd = exec.Command("xdg-open", a.dashboardURL)
	}
	a.hideWindow(cmd)
	cmd.Start()
}

func (a *App) onExit() {
	if a.mihomoCmd != nil && a.mihomoCmd.Process != nil {
		fmt.Println("关闭 mihomo...")
		_ = a.mihomoCmd.Process.Kill()
		_, _ = a.mihomoCmd.Process.Wait()
	}
	a.disableSystemProxy()
	_ = a.setTun(false)
	fmt.Println("TUN 模式已自动关闭")
}

func (a *App) appDir() string {
	exePath, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exePath)
}

// ==================== 主入口 ====================
func main() {
	ensureSingleInstance()
	app := NewApp()
	systray.Run(app.onReady, app.onExit)
}
