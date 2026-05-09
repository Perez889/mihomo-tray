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
	"github.com/webview/webview_go"
	"golang.org/x/sys/windows"
	"gopkg.in/yaml.v3"
)

// ==================== 版本号 ====================
var Version = "1.3.3"

// ==================== 图标嵌入 ====================
//go:embed icon/tray.ico
var trayDefaultIcon []byte

//go:embed icon/mihomo_proxy.ico
var trayProxyIcon []byte

//go:embed icon/mihomo_tun.ico
var trayTunIcon []byte

//go:embed icon/mihomo_all.ico
var trayAllIcon []byte

//go:embed icon/zashboard.ico
var zashboardIcon []byte

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
	exe, _ := os.Executable()
	verbPtr, _ := windows.UTF16PtrFromString("runas")
	exePtr, _ := windows.UTF16PtrFromString(exe)
	argPtr, _ := windows.UTF16PtrFromString(strings.Join(os.Args[1:], " "))
	windows.ShellExecute(0, verbPtr, exePtr, argPtr, nil, windows.SW_NORMAL)
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
	iconDefault          []byte
	iconProxy            []byte
	iconTun              []byte
	iconAll              []byte
	firstRun             bool
}

// ==================== NewApp ====================
func NewApp() *App {
	app := &App{
		isSystemProxyEnabled: false,
		isTUNEnabled:         false,
		mixedPort:            "1081",
		controllerAddr:       "127.0.0.1:9090",
		secret:               "",
		currentMode:          "",
		firstRun:             false,
		iconDefault:          trayDefaultIcon,
		iconProxy:            trayProxyIcon,
		iconTun:              trayTunIcon,
		iconAll:              trayAllIcon,
	}
	app.initLogger()
	app.loadConfig()
	app.checkFirstRun()
	return app
}

// ==================== 首次运行检测 ====================
func (a *App) checkFirstRun() {
	flagFile := filepath.Join(a.appDir(), ".nettray_first_run")
	if _, err := os.Stat(flagFile); os.IsNotExist(err) {
		a.firstRun = true
		_ = os.WriteFile(flagFile, []byte(time.Now().Format("2006-01-02")), 0666)
		a.log("检测到首次运行，将创建 Zashboard 快捷方式")
	}
}

// ==================== 创建快捷方式 ====================
func (a *App) createZashboardShortcuts() {
	if !a.firstRun {
		return
	}

	exePath, _ := os.Executable()
	baseDir := a.appDir()
	desktopPath := filepath.Join(os.Getenv("USERPROFILE"), "Desktop")
	startMenuPath := filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs`)

	icoPath := filepath.Join(baseDir, "zashboard.ico")
	if len(zashboardIcon) > 0 {
		_ = os.WriteFile(icoPath, zashboardIcon, 0666)
	}

	shortcutName := "Zashboard.lnk"

	a.createShortcut(filepath.Join(desktopPath, shortcutName), exePath, "--open-zashboard", "Zashboard 面板", icoPath)
	a.createShortcut(filepath.Join(startMenuPath, shortcutName), exePath, "--open-zashboard", "Zashboard 面板", icoPath)

	a.log("✅ 已创建 Zashboard 桌面和开始菜单快捷方式")
}

func (a *App) createShortcut(lnkPath, target, args, desc, iconPath string) {
	script := fmt.Sprintf(`$WshShell = New-Object -ComObject WScript.Shell
$Shortcut = $WshShell.CreateShortcut("%s")
$Shortcut.TargetPath = "%s"
$Shortcut.Arguments = "%s"
$Shortcut.WorkingDirectory = "%s"
$Shortcut.Description = "%s"
$Shortcut.IconLocation = "%s,0"
$Shortcut.WindowStyle = 1
$Shortcut.Save()`, lnkPath, target, args, filepath.Dir(target), desc, iconPath)

	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script)
	a.hideWindow(cmd)
	_ = cmd.Run()
}

// ==================== WebView2 打开面板 ====================
func (a *App) openEmbeddedDashboard() {
	if a.dashboardURL == "" {
		a.buildDashboardURL()
	}

	w := webview.New(true)
	defer w.Destroy()

	w.SetTitle("Zashboard - Mihomo")
	w.SetSize(1420, 980, webview.HintNone)
	w.Navigate(a.dashboardURL)

	a.logf("WebView2 Zashboard 已打开 → %s", a.dashboardURL)
	w.Run()
}

// ==================== 日志 ====================
func (a *App) initLogger() {
	baseDir := a.appDir()
	logPath := filepath.Join(baseDir, "net-tray.log")
	_ = os.Remove(logPath)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
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
		switch v := s.(type) {
		case string:
			a.secret = strings.TrimSpace(v)
		case int:
			a.secret = strconv.Itoa(v)
		case float64:
			a.secret = strconv.Itoa(int(v))
		}
	}

	if ui, ok := cfg["external-ui"]; ok {
		if uiStr, ok := ui.(string); ok && uiStr != "" {
			a.externalUI = strings.TrimSpace(uiStr)
		}
	}

	if name, ok := cfg["external-ui-name"]; ok {
		if nameStr, ok := name.(string); ok && nameStr != "" {
			a.externalUIName = strings.TrimSpace(nameStr)
		}
	}

	if tun, ok := cfg["tun"]; ok {
		if tunMap, ok := tun.(map[string]interface{}); ok {
			if enable, ok := tunMap["enable"].(bool); ok {
				a.isTUNEnabled = enable
			} else if enableStr, ok := tunMap["enable"].(string); ok {
				a.isTUNEnabled = strings.ToLower(strings.TrimSpace(enableStr)) == "true"
			}
		}
	} else {
		a.isTUNEnabled = false
	}

	a.buildDashboardURL()
	a.logf("配置加载成功 → 端口: %s | 控制器: %s | TUN: %v", a.mixedPort, a.controllerAddr, a.isTUNEnabled)
}

func (a *App) buildDashboardURL() {
	base := fmt.Sprintf("http://%s/ui", a.controllerAddr)
	if a.externalUIName != "" {
		a.dashboardURL = fmt.Sprintf("%s/%s", base, a.externalUIName)
	} else if a.externalUI != "" {
		uiPath := strings.TrimRight(strings.TrimSpace(a.externalUI), "/\\")
		a.dashboardURL = fmt.Sprintf("%s/%s", base, uiPath)
	} else {
		a.dashboardURL = fmt.Sprintf("%s/zashboard", base)
	}
	if a.secret != "" {
		a.dashboardURL += "?secret=" + a.secret
	}
}

// ==================== 托盘图标 ====================
func (a *App) updateTrayIcon() {
	var iconToUse []byte
	switch {
	case a.isSystemProxyEnabled && a.isTUNEnabled:
		iconToUse = a.iconAll
	case a.isSystemProxyEnabled:
		iconToUse = a.iconProxy
	case a.isTUNEnabled:
		iconToUse = a.iconTun
	default:
		iconToUse = a.iconDefault
	}
	systray.SetIcon(iconToUse)
}

// ==================== onReady ====================
func (a *App) onReady() {
	a.updateTrayIcon()
	systray.SetTooltip("Mihomo Lite\n轻量托盘工具")

	mOpen := systray.AddMenuItem("打开面板", "打开 Zashboard (WebView2)")
	systray.AddSeparator()

	mMode := systray.AddMenuItem("出站模式", "切换代理模式")
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

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				go a.openEmbeddedDashboard()
			case <-mRestart.ClickedCh:
				a.restartMihomo()
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

	if a.firstRun {
		go func() {
			time.Sleep(1500 * time.Millisecond)
			a.createZashboardShortcuts()
		}()
	}

	go a.startMihomo()

	go func() {
		for {
			if a.isPortOpen(a.controllerAddr) {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		a.syncTunStateWithRetry(mTun, 30)
		a.syncModeStateWithRetry(mRule, mGlobal, mDirect, 15)
		a.updateTrayIcon()
	}()
}

// ==================== 模式相关 ====================
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
	req, _ := http.NewRequest("PATCH", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if a.secret != "" {
		req.Header.Set("Authorization", "Bearer "+a.secret)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err == nil && resp.StatusCode/100 == 2 {
		a.currentMode = mode
		a.updateModeUI(mode, mRule, mGlobal, mDirect)
		a.logf("已切换到 %s 模式", strings.ToUpper(mode))
	}
	if resp != nil {
		resp.Body.Close()
	}
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
	a.log("【警告】多次尝试后仍无法同步 TUN 状态")
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
	a.updateTrayIcon()
	go a.asyncToggleTun(newEnable)
}

func (a *App) asyncToggleTun(expectedState bool) {
	if err := a.setTun(expectedState); err != nil {
		a.logf("TUN PATCH 请求失败: %v", err)
		a.tunMutex.Lock()
		a.isTUNEnabled = !expectedState
		a.tunMutex.Unlock()
		a.updateTrayIcon()
		return
	}
	a.logf("TUN 切换完成，当前实际状态: %v", expectedState)
	a.updateTrayIcon()
}

func (a *App) setTun(enable bool) error {
	url := fmt.Sprintf("http://%s/configs", a.controllerAddr)
	body := fmt.Sprintf(`{"tun":{"enable":%v}}`, enable)
	req, _ := http.NewRequest("PATCH", url, strings.NewReader(body))
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
			a.log("【警告】TUN 模式已开启，同时开启系统代理可能导致部分流量绕过 TUN 规则！")
		}
		a.enableSystemProxy(proxyAddr)
		a.log("系统代理已开启")
	} else {
		a.disableSystemProxy()
		a.log("系统代理已关闭")
	}
	a.updateProxyMenu(m)
	a.updateTrayIcon()
}

func (a *App) enableSystemProxy(proxyAddr string) {
	a.logf("开启系统代理 → %s", proxyAddr)
	cmdStr := fmt.Sprintf(`Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyServer -Value "%s"; Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyEnable -Value 1`, proxyAddr)
	cmd := exec.Command("powershell", "-Command", cmdStr)
	a.hideWindow(cmd)
	_ = cmd.Run()
}

func (a *App) disableSystemProxy() {
	a.log("关闭系统代理")
	cmd := exec.Command("powershell", "-Command", `Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyEnable -Value 0`)
	a.hideWindow(cmd)
	_ = cmd.Run()
}

func (a *App) hideWindow(cmd *exec.Cmd) {
	if runtime.GOOS == "windows" && cmd != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:    true,
			CreationFlags: 0x08000000,
		}
	}
}

// ==================== Mihomo 控制 ====================
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
	exePath := filepath.Join(baseDir, "mihomo.exe")
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

func (a *App) onExit() {
	a.log("正在退出 NetTray...")
	if a.mihomoCmd != nil && a.mihomoCmd.Process != nil {
		_ = a.mihomoCmd.Process.Kill()
		_, _ = a.mihomoCmd.Process.Wait()
	}
	a.disableSystemProxy()
	if a.isTUNEnabled {
		_ = a.setTun(false)
	}
	if singleInstanceMutex != 0 {
		windows.CloseHandle(singleInstanceMutex)
	}
	a.log("NetTray 已安全退出")
}

func (a *App) appDir() string {
	exePath, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exePath)
}

// ==================== 命令行参数处理 ====================
func handleCommandLine() {
	if len(os.Args) > 1 && os.Args[1] == "--open-zashboard" {
		fmt.Println("【快捷方式启动】检测到 --open-zashboard 参数")
		app := NewApp()

		if !app.isPortOpen(app.controllerAddr) {
			fmt.Println("【快捷方式启动】mihomo 未运行，自动启动...")
			app.startMihomoForce()
			time.Sleep(2800 * time.Millisecond)
		}

		app.openEmbeddedDashboard()
		os.Exit(0)
	}
}

// ==================== main ====================
func main() {
	fmt.Println("NetTray 启动，命令行参数:", os.Args)

	ensureSingleInstance()

	if runtime.GOOS == "windows" && !isAdmin() {
		fmt.Println("当前未以管理员权限运行，正在请求 UAC 提升...")
		runAsAdmin()
		time.Sleep(1 * time.Second)
		os.Exit(0)
	}

	handleCommandLine()

	app := NewApp()
	systray.Run(app.onReady, app.onExit)
}
