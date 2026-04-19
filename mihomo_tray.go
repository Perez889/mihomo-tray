package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/getlantern/systray"
	"golang.org/x/sys/windows"
	"gopkg.in/yaml.v3"
)

// ==================== 版本号 ====================
var Version = "1.2.7"

// ==================== 图标嵌入 ====================
//go:embed icon/tray.ico
var trayDefaultIcon []byte

//go:embed icon/mihomo_proxy.ico
var trayProxyIcon []byte

//go:embed icon/mihomo_tun.ico
var trayTunIcon []byte

//go:embed icon/mihomo_all.ico
var trayAllIcon []byte

// ==================== 单实例控制 ====================
var singleInstanceMutex windows.Handle

func ensureSingleInstance() {
	name, _ := windows.UTF16PtrFromString("Global\\NetTraySingleton")
	h, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		fmt.Println("创建互斥锁失败:", err)
		os.Exit(1)
	}
	singleInstanceMutex = h
	if windows.GetLastError() == windows.ERROR_ALREADY_EXISTS {
		fmt.Println("NetTray 已经在运行")
		os.Exit(0)
	}
}

// ==================== 检查是否以管理员权限运行 ====================
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

// ==================== 以管理员权限重新启动自身 ====================
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
	err = windows.ShellExecute(0, verbPtr, exePtr, argPtr, cwdPtr, windows.SW_NORMAL)
	if err != nil {
		fmt.Println("请求管理员权限失败:", err)
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
	currentMode          string
	tunMutex             sync.Mutex
	logger               *log.Logger
	externalUI           string
	externalUIName       string

	// 图标缓存
	iconDefault []byte
	iconProxy   []byte
	iconTun     []byte
	iconAll     []byte
}

func NewApp() *App {
	app := &App{
		isSystemProxyEnabled: false,
		isTUNEnabled:         false,
		mixedPort:            "1081",
		controllerAddr:       "127.0.0.1:9090",
		secret:               "",
		currentMode:          "",

		// 初始化图标
		iconDefault: trayDefaultIcon,
		iconProxy:   trayProxyIcon,
		iconTun:     trayTunIcon,
		iconAll:     trayAllIcon,
	}
	app.initLogger()
	app.loadConfig()
	return app
}

// ==================== 日志初始化（退出后保留最后一次日志） ====================
func (a *App) initLogger() {
	baseDir := a.appDir()
	logPath := filepath.Join(baseDir, "net-tray.log")
	// 启动时先删除旧日志（保留最后一次）
	if err := os.Remove(logPath); err != nil && !os.IsNotExist(err) {
		fmt.Printf("删除旧日志文件失败: %v\n", err)
	}
	// 创建新的日志文件
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		fmt.Printf("日志文件创建失败: %v，使用标准输出\n", err)
		a.logger = log.New(os.Stdout, "[NetTray] ", log.LstdFlags)
		return
	}
	a.logger = log.New(f, "[NetTray] ", log.LstdFlags)
	a.logger.Println("=== NetTray 启动 ===")
	a.logger.Printf("版本: %s | 目录: %s", Version, baseDir)
}

func (a *App) log(msg string) {
	if a.logger != nil {
		a.logger.Println(msg)
	}
}

func (a *App) logf(format string, args ...interface{}) {
	if a.logger != nil {
		a.logger.Printf(format, args...)
	}
}

// ==================== 配置加载 ====================
func (a *App) loadConfig() {
	baseDir := a.appDir()
	configPath := filepath.Join(baseDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		a.log("警告：无法读取 config.yaml，使用默认值")
		a.buildDashboardURL()
		return
	}
	var cfg map[string]interface{}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		a.log("警告：解析 config.yaml 失败，使用默认值")
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
		switch v := s.(type) {
		case string:
			a.secret = strings.TrimSpace(v)
		case int:
			a.secret = strconv.Itoa(v)
		case float64:
			a.secret = strconv.Itoa(int(v))
		default:
			// 保持默认空字符串
		}
	}
	// external-ui
	if ui, ok := cfg["external-ui"]; ok {
		if uiStr, ok := ui.(string); ok && uiStr != "" {
			a.externalUI = strings.TrimSpace(uiStr)
			a.logf("读取 external-ui: %s", a.externalUI)
		}
	}
	// external-ui-name
	if name, ok := cfg["external-ui-name"]; ok {
		if nameStr, ok := name.(string); ok && nameStr != "" {
			a.externalUIName = strings.TrimSpace(nameStr)
			a.logf("读取 external-ui-name: %s", a.externalUIName)
		}
	}
	// external-ui-url
	if url, ok := cfg["external-ui-url"]; ok {
		if urlStr, ok := url.(string); ok && urlStr != "" {
			a.logf("检测到 external-ui-url: %s", urlStr)
		}
	}
	// TUN 配置
	if tun, ok := cfg["tun"]; ok {
		if tunMap, ok := tun.(map[string]interface{}); ok {
			if enable, ok := tunMap["enable"].(bool); ok {
				a.isTUNEnabled = enable
				a.logf("从 config.yaml 读取 TUN 配置 → enable: %v", enable)
			} else if enableStr, ok := tunMap["enable"].(string); ok {
				a.isTUNEnabled = strings.ToLower(strings.TrimSpace(enableStr)) == "true"
				a.logf("从 config.yaml 读取 TUN 配置 → enable: %v (string)", a.isTUNEnabled)
			}
		}
	} else {
		a.log("config.yaml 中未找到 tun 配置，默认 TUN: false")
		a.isTUNEnabled = false
	}
	a.buildDashboardURL()
	a.logf("配置加载成功 → 端口: %s | 控制器: %s | Secret: %q | TUN: %v | external-ui-name: %s",
		a.mixedPort, a.controllerAddr, a.secret, a.isTUNEnabled, a.externalUIName)
}

// ==================== 构建 Dashboard URL ====================
func (a *App) buildDashboardURL() {
	base := fmt.Sprintf("http://%s/ui", a.controllerAddr)
	if a.externalUIName != "" {
		a.dashboardURL = fmt.Sprintf("%s/%s", base, a.externalUIName)
		if a.secret != "" {
			a.dashboardURL += "?secret=" + a.secret
		}
		a.logf("使用 external-ui-name 构建面板地址 → %s", a.dashboardURL)
		return
	}
	if a.externalUI != "" {
		uiPath := strings.TrimRight(strings.TrimSpace(a.externalUI), "/\\")
		a.dashboardURL = fmt.Sprintf("%s/%s", base, uiPath)
		if a.secret != "" {
			a.dashboardURL += "?secret=" + a.secret
		}
		a.logf("使用 external-ui 构建面板地址 → %s", a.dashboardURL)
		return
	}
	if a.secret != "" {
		a.dashboardURL = fmt.Sprintf("%s/zashboard?secret=%s", base, a.secret)
	} else {
		a.dashboardURL = fmt.Sprintf("%s/zashboard", base)
	}
	a.log("未检测到 external-ui 配置，使用默认 zashboard")
}

// ==================== 更新托盘图标（核心新增函数） ====================
func (a *App) updateTrayIcon() {
	var iconToUse []byte

	proxyOn := a.isSystemProxyEnabled
	tunOn := a.isTUNEnabled

	switch {
	case proxyOn && tunOn:
		iconToUse = a.iconAll
	case proxyOn:
		iconToUse = a.iconProxy
	case tunOn:
		iconToUse = a.iconTun
	default:
		iconToUse = a.iconDefault
	}

	if runtime.GOOS == "windows" {
		systray.SetIcon(iconToUse)
	} else {
		systray.SetTemplateIcon(iconToUse, iconToUse)
	}
}

// ==================== UI 初始化 ====================
func (a *App) onReady() {
	// 设置初始图标
	a.updateTrayIcon()

	systray.SetTooltip("Mihomo Lite\n轻量托盘工具")

	mOpen := systray.AddMenuItem("打开面板", "打开 Dashboard")
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

	// 启动后同步状态（自动等待 mihomo ready）
	go func() {
		for {
			if a.isPortOpen(a.controllerAddr) {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		a.syncTunStateWithRetry(mTun, 30)
		a.syncModeStateWithRetry(mRule, mGlobal, mDirect, 15)
		a.updateTrayIcon() // 同步完成后刷新一次图标
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
	a.log("警告：无法同步代理模式状态")
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
		a.logf("设置模式失败: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if a.secret != "" {
		req.Header.Set("Authorization", "Bearer "+a.secret)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		a.logf("切换到 %s 模式失败", mode)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		a.logf("切换到 %s 模式失败（HTTP %d）", mode, resp.StatusCode)
		return
	}
	a.currentMode = mode
	a.updateModeUI(mode, mRule, mGlobal, mDirect)
	a.logf("已切换到 %s 模式", strings.ToUpper(mode))
}

// ==================== TUN 相关 ====================
func (a *App) syncTunStateWithRetry(m *systray.MenuItem, maxRetries int) {
	a.log("开始同步 TUN 状态...")
	for i := 0; i < maxRetries; i++ {
		if a.fetchAndUpdateTunState(m) {
			a.logf("TUN 状态同步成功 → 当前实际状态: %v", a.isTUNEnabled)
			return
		}
		time.Sleep(900 * time.Millisecond)
	}
	a.log("=================================================================")
	a.log("【警告】多次尝试后仍无法同步 TUN 状态")
	a.log("=================================================================")
}

func (a *App) fetchAndUpdateTunState(m *systray.MenuItem) bool {
	a.tunMutex.Lock()
	defer a.tunMutex.Unlock()
	if a.isTUNEnabled {
		m.Check()
	} else {
		m.Uncheck()
	}
	return true
}

func (a *App) toggleTun(m *systray.MenuItem) {
	a.tunMutex.Lock()
	newEnable := !a.isTUNEnabled
	a.isTUNEnabled = newEnable
	if newEnable {
		m.Check()
	} else {
		m.Uncheck()
	}
	a.tunMutex.Unlock()

	a.logf("尝试切换 TUN 模式 → %v", newEnable)
	a.updateTrayIcon() // 立即更新图标
	go a.asyncToggleTun(newEnable)
}

func (a *App) asyncToggleTun(expectedState bool) {
	if err := a.setTun(expectedState); err != nil {
		a.logf("TUN PATCH 请求失败: %v", err)
		a.tunMutex.Lock()
		a.isTUNEnabled = !expectedState
		a.tunMutex.Unlock()
		a.updateTrayIcon() // 失败后恢复图标
		return
	}
	a.logf("TUN 切换完成，当前实际状态: %v", expectedState)
	a.updateTrayIcon() // 成功后更新图标
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
	client := &http.Client{Timeout: 6 * time.Second}
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
		if !a.isPortOpen("127.0.0.1:" + a.mixedPort) {
			a.log("检测到 mihomo 未运行，自动启动...")
			a.startMihomo()
			for i := 0; i < 8; i++ {
				if a.isPortOpen("127.0.0.1:" + a.mixedPort) {
					break
				}
				time.Sleep(400 * time.Millisecond)
			}
			if !a.isPortOpen("127.0.0.1:" + a.mixedPort) {
				a.log("mihomo 启动失败，无法开启系统代理")
				a.isSystemProxyEnabled = false
				a.updateProxyMenu(m)
				a.updateTrayIcon()
				return
			}
		}
		if a.isTUNEnabled {
			a.log("【警告】TUN 模式已开启，同时开启系统代理可能导致部分流量绕过 TUN 规则！建议只使用其中一种方式")
		}
		a.enableSystemProxy(proxyAddr)
		a.log("系统代理已开启")
	} else {
		a.disableSystemProxy()
		a.log("系统代理已关闭")
	}

	a.updateProxyMenu(m)
	a.updateTrayIcon() // 状态改变后更新图标
}

func (a *App) enableSystemProxy(proxyAddr string) {
	a.logf("开启系统代理 → %s", proxyAddr)
	if runtime.GOOS == "windows" {
		cmdStr := fmt.Sprintf(`Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyServer -Value "%s"; Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyEnable -Value 1`, proxyAddr)
		cmd := exec.Command("powershell", "-Command", cmdStr)
		a.hideWindow(cmd)
		_ = cmd.Run()
	}
}

func (a *App) disableSystemProxy() {
	a.log("关闭系统代理")
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
		a.log("mihomo 已在运行")
		return
	}
	if a.isPortOpen("127.0.0.1:" + a.mixedPort) {
		a.log("检测到端口已占用，跳过启动 mihomo")
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
		a.logf("启动失败: %v", err)
		return
	}
	a.mihomoCmd = cmd
	a.log("mihomo 启动成功")
}

func (a *App) restartMihomo() {
	a.log("正在重启 mihomo...")
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
	_ = cmd.Start()
	a.logf("已打开面板: %s", a.dashboardURL)
}

func (a *App) onExit() {
	a.log("正在退出 NetTray...")
	if a.mihomoCmd != nil && a.mihomoCmd.Process != nil {
		a.log("关闭 mihomo...")
		_ = a.mihomoCmd.Process.Kill()
		_, _ = a.mihomoCmd.Process.Wait()
	}
	a.disableSystemProxy()
	if a.isTUNEnabled {
		a.log("尝试关闭 TUN 模式...")
		_ = a.setTun(false)
	}
	if singleInstanceMutex != 0 {
		windows.CloseHandle(singleInstanceMutex)
	}
	a.log("NetTray 已安全退出")
}

// ==================== 目录 ====================
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
