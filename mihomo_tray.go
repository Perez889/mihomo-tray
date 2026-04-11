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

// ==================== 管理员权限相关 ====================
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

	// 菜单项（解决状态不同步）
	mProxy *systray.MenuItem
	mTun   *systray.MenuItem
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

	// external-controller（健壮处理）
	if ctrl, ok := cfg["external-controller"]; ok {
		if ctrlStr, ok := ctrl.(string); ok && ctrlStr != "" {
			ctrlStr = strings.TrimSpace(ctrlStr)
			ctrlStr = strings.Trim(ctrlStr, `"'`)
			ctrlStr = strings.TrimPrefix(ctrlStr, "http://")
			ctrlStr = strings.TrimPrefix(ctrlStr, "https://")
			if strings.HasPrefix(ctrlStr, "0.0.0.0") {
				ctrlStr = "127.0.0.1" + ctrlStr[7:]
			}
			a.controllerAddr = ctrlStr
		}
	}

	// secret
	if s, ok := cfg["secret"]; ok {
		if secretStr, ok := s.(string); ok {
			a.secret = strings.TrimSpace(secretStr)
		}
	}

	// TUN 配置
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

	a.mProxy = systray.AddMenuItemCheckbox("系统代理", "点击切换系统代理开关", false)
	systray.AddSeparator()
	a.mTun = systray.AddMenuItemCheckbox("虚拟网卡 (TUN)", "切换 TUN 模式", a.isTUNEnabled)
	systray.AddSeparator()

	mRestart := systray.AddMenuItem("重启内核", "重启 Mihomo")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出应用", "退出并关闭 mihomo")

	a.updateProxyMenu(a.mProxy)

	// 启动后同步
	go func() {
		time.Sleep(1800 * time.Millisecond)
		a.syncTunStateWithRetry(a.mTun, 15)
		a.syncModeStateWithRetry(mRule, mGlobal, mDirect, 12)
	}()

	go func() {
		for {
			select {
			case <-mRestart.ClickedCh:
				a.restartMihomo()
			case <-mOpen.ClickedCh:
				a.openDashboard()
			case <-a.mProxy.ClickedCh:
				a.toggleSystemProxy()
			case <-mRule.ClickedCh:
				a.setMode("rule", mRule, mGlobal, mDirect)
			case <-mGlobal.ClickedCh:
				a.setMode("global", mRule, mGlobal, mDirect)
			case <-mDirect.ClickedCh:
				a.setMode("direct", mRule, mGlobal, mDirect)
			case <-a.mTun.ClickedCh:
				a.toggleTun()
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()

	go a.startMihomo()
	go a.monitorMihomo() // 崩溃自动重启
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
	return a.apiGet("/configs", func(data map[string]interface{}) bool {
		if mode, ok := data["mode"].(string); ok {
			a.currentMode = mode
			a.updateModeUI(mode, mRule, mGlobal, mDirect)
			return true
		}
		return false
	})
}

func (a *App) setMode(mode string, mRule, mGlobal, mDirect *systray.MenuItem) {
	body := fmt.Sprintf(`{"mode":"%s"}`, mode)
	if a.apiPatch("/configs", body) == nil {
		a.currentMode = mode
		a.updateModeUI(mode, mRule, mGlobal, mDirect)
		fmt.Printf("已切换到 %s 模式\n", strings.ToUpper(mode))
	}
}

// ==================== TUN 相关 ====================
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
	return a.apiGet("/configs", func(data map[string]interface{}) bool {
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
	})
}

func (a *App) toggleTun() {
	newEnable := !a.isTUNEnabled
	fmt.Printf("尝试切换 TUN 模式 → %v\n", newEnable)

	if err := a.setTun(newEnable); err != nil {
		fmt.Printf("TUN PATCH 请求失败: %v\n", err)
		a.fetchAndUpdateTunState(a.mTun)
		return
	}

	time.Sleep(1500 * time.Millisecond)
	a.fetchAndUpdateTunState(a.mTun)
}

func (a *App) setTun(enable bool) error {
	body := fmt.Sprintf(`{"tun":{"enable":%v}}`, enable)
	return a.apiPatch("/configs", body)
}

// ==================== 系统代理 ====================
func (a *App) updateProxyMenu(m *systray.MenuItem) {
	if a.isSystemProxyEnabled {
		m.Check()
	} else {
		m.Uncheck()
	}
}

func (a *App) toggleSystemProxy() {
	a.isSystemProxyEnabled = !a.isSystemProxyEnabled
	proxyAddr := "127.0.0.1:" + a.mixedPort

	if a.isSystemProxyEnabled {
		if a.isTUNEnabled {
			fmt.Println("【警告】TUN 已开启，同时使用系统代理可能导致部分流量绕过 TUN 规则！")
		}
		a.enableSystemProxy(proxyAddr)
		fmt.Println("系统代理已开启")
	} else {
		a.disableSystemProxy()
		fmt.Println("系统代理已关闭")
	}
	a.updateProxyMenu(a.mProxy)
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

// ==================== 统一 API 调用（解决 401/404） ====================
func (a *App) apiGet(endpoint string, handler func(map[string]interface{}) bool) bool {
	url := fmt.Sprintf("http://%s%s", a.controllerAddr, endpoint)
	req, _ := http.NewRequest("GET", url, nil)
	if a.secret != "" {
		req.Header.Set("Authorization", "Bearer "+a.secret)
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case 401:
		fmt.Println("错误：Secret 不正确，请检查 config.yaml 中的 secret 配置")
		return false
	case 404:
		fmt.Println("错误：Mihomo external-controller 未开启，请检查 config.yaml")
		return false
	case 200:
		// 正常继续
	default:
		return false
	}

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return false
	}
	return handler(data)
}

func (a *App) apiPatch(endpoint, body string) error {
	url := fmt.Sprintf("http://%s%s", a.controllerAddr, endpoint)
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

	switch resp.StatusCode {
	case 401:
		fmt.Println("错误：Secret 不正确，请检查 config.yaml 中的 secret 配置")
		return fmt.Errorf("secret 错误")
	case 404:
		fmt.Println("错误：Mihomo external-controller 未开启，请检查 config.yaml")
		return fmt.Errorf("API 未开启")
	}

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP 状态码: %d", resp.StatusCode)
	}
	return nil
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
	exePath := filepath.Join(baseDir, "mihomo.exe")

	if _, err := os.Stat(exePath); os.IsNotExist(err) {
		fmt.Printf("【严重错误】未找到 mihomo.exe！\n请确保 mihomo.exe 与本程序放在同一目录下\n路径: %s\n", exePath)
		return
	}

	cmd := exec.Command(exePath, "-d", ".")
	cmd.Dir = baseDir
	a.hideWindow(cmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		fmt.Printf("启动 mihomo 失败: %v\n", err)
		return
	}
	a.mihomoCmd = cmd
	fmt.Println("mihomo 启动成功")
}

// ==================== 崩溃监控与重启 ====================
func (a *App) monitorMihomo() {
	ticker := time.NewTicker(8 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if a.mihomoCmd == nil || a.mihomoCmd.Process == nil {
			continue
		}
		if !a.isRunning(a.mihomoCmd) {
			fmt.Println("检测到 mihomo 已崩溃，正在自动重启...")
			a.restartMihomo()
		}
	}
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
	fmt.Println("正在退出程序...")
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
