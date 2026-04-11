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

// ==================== 管理员权限 ====================
func isAdmin() bool {
	var sid *windows.SID
	err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0,
		&sid,
	)
	if err != nil {
		return false
	}
	defer windows.FreeSid(sid)
	token := windows.GetCurrentProcessToken()
	member, err := token.IsMember(sid)
	if err != nil {
		return false
	}
	return member
}

func runAsAdmin() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Println("无法获取可执行文件路径:", err)
		return
	}
	verbPtr, _ := windows.UTF16PtrFromString("runas")
	exePtr, _ := windows.UTF16PtrFromString(exe)
	cwdPtr, _ := windows.UTF16PtrFromString("")
	argPtr, _ := windows.UTF16PtrFromString(strings.Join(os.Args[1:], " "))
	_ = windows.ShellExecute(0, verbPtr, exePtr, argPtr, cwdPtr, windows.SW_NORMAL)
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
	currentMode          string
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

	if ctrl, ok := cfg["external-controller"]; ok {
		if ctrlStr, ok := ctrl.(string); ok && ctrlStr != "" {
			a.controllerAddr = strings.TrimSpace(ctrlStr)
		}
	}

	if s, ok := cfg["secret"]; ok {
		if secretStr, ok := s.(string); ok {
			a.secret = strings.TrimSpace(secretStr)
		}
	}

	// TUN 配置读取
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

	mMode := systray.AddMenuItem("出站模式", "切换代理模式")
	systray.AddSeparator()
	mRule := mMode.AddSubMenuItemCheckbox("规则", "Rule 模式", false)
	mGlobal := mMode.AddSubMenuItemCheckbox("全局", "Global 模式", false)
	mDirect := mMode.AddSubMenuItemCheckbox("直连", "Direct 模式", false)

	mProxy := systray.AddMenuItemCheckbox("系统代理", "点击切换系统代理开关", false)
	systray.AddSeparator()
	mTun := systray.AddMenuItemCheckbox("虚拟网卡", "切换 TUN 模式", a.isTUNEnabled)
	systray.AddSeparator()

	mRestart := systray.AddMenuItem("重启内核", "重启 Mihomo")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出应用", "退出并关闭 mihomo")

	a.updateProxyMenu(mProxy)

	// 启动后同步状态
	go func() {
		time.Sleep(2200 * time.Millisecond)
		a.syncTunStateWithRetry(mTun, 18)
		a.syncModeStateWithRetry(mRule, mGlobal, mDirect, 12)
	}()

	go a.monitorMihomo() // ← 防崩溃自动重启

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

// ==================== 代理模式相关 ====================
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
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
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

// ==================== TUN 相关（已优化点击延迟） ====================
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
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
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

	// 立即给视觉反馈，减少感知延迟
	if newEnable {
		m.Check()
	} else {
		m.Uncheck()
	}

	if err := a.setTun(newEnable); err != nil {
		fmt.Printf("TUN PATCH 请求失败: %v\n", err)
		a.fetchAndUpdateTunState(m)
		return
	}

	// 缩短等待时间
	time.Sleep(800 * time.Millisecond)
	a.fetchAndUpdateTunState(m)

	fmt.Printf("TUN 切换完成，当前实际状态: %v\n", a.isTUNEnabled)
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

// ==================== 系统代理 ====================
func (a *App) updateProxyMenu(m *systray.MenuItem) {
	if a.isSystemProxyEnabled {
		m.Check()
	} else {
		m.Uncheck()
	}
}

func (a *App) toggleSystemProxy(m *systray.MenuItem) {
	a.isSystemProxyEnabled = !a.isSystemProxyEnabled
	proxyAddr := "127.0.0.1:" + a.mixedPort

	if a.isSystemProxyEnabled {
		if a.isTUNEnabled {
			fmt.Println("【警告】TUN 模式已开启，同时开启系统代理可能导致部分流量绕过 TUN 规则！")
		}
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
		cmdStr := fmt.Sprintf(`Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyServer -Value "%s"; Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyEnable -Value 1`, proxyAddr)
		cmd := exec.Command("powershell", "-Command", cmdStr)
		a.hideWindow(cmd)
		_ = cmd.Run()
	}
}

func (a *App) disableSystemProxy() {
	fmt.Println("关闭系统代理")
	if runtime.GOOS == "windows" {
		cmd := exec.Command("powershell", "-Command", `Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyEnable -Value 0`)
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

// ==================== Mihomo 核心控制 ====================
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
	exeName := "mihomo.exe"
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

// ==================== 防崩溃自动重启监控（新增） ====================
func (a *App) monitorMihomo() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if a.mihomoCmd == nil || a.mihomoCmd.Process == nil {
			continue
		}
		if !a.isRunning(a.mihomoCmd) {
			fmt.Println("检测到 mihomo 已崩溃 → 自动重启")
			a.restartMihomo()
		}
	}
}

func (a *App) onExit() {
	fmt.Println("正在退出...")
	if a.mihomoCmd != nil && a.mihomoCmd.Process != nil {
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
	if runtime.GOOS == "windows" && !isAdmin() {
		fmt.Println("当前未以管理员权限运行，正在请求 UAC 提升...")
		runAsAdmin()
		time.Sleep(1 * time.Second)
		os.Exit(0)
	}

	app := NewApp()
	systray.Run(app.onReady, app.onExit)
}
