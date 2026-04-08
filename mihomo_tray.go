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

//go:embed clash.ico
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
	isTUNEnabled         bool // 新增：统一管理 TUN 真实状态
	mixedPort            string
	controllerAddr       string
	secret               string
	dashboardURL         string
}

func NewApp() *App {
	app := &App{
		isSystemProxyEnabled: false,
		isTUNEnabled:         false,
		mixedPort:            "1081",
		controllerAddr:       "127.0.0.1:9090",
		secret:               "",
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

	a.buildDashboardURL()
	fmt.Printf("配置加载成功 → 系统代理端口: %s | 控制器: %s | Secret: %q\n",
		a.mixedPort, a.controllerAddr, a.secret)
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

	mRestart := systray.AddMenuItem("重启内核", "重启 Mihomo")
	systray.AddSeparator()
	mOpen := systray.AddMenuItem("打开面板", "打开 Zashboard")
	systray.AddSeparator()

	mProxy := systray.AddMenuItemCheckbox("系统代理", "点击切换系统代理开关", false)
	systray.AddSeparator()
	mTun := systray.AddMenuItemCheckbox("虚拟网卡", "切换 TUN 模式（全局透明代理）", false)
	systray.AddSeparator()

	mQuit := systray.AddMenuItem("退出应用", "退出并关闭 mihomo")

	a.updateProxyMenu(mProxy)

	// 启动后延迟同步 TUN 状态（带重试，更可靠）
	go a.syncTunStateWithRetry(mTun, 10)

	go func() {
		for {
			select {
			case <-mRestart.ClickedCh:
				a.restartMihomo()
			case <-mOpen.ClickedCh:
				a.openDashboard()
			case <-mProxy.ClickedCh:
				a.toggleSystemProxy(mProxy)
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

// ==================== TUN 状态同步（带重试） ====================
func (a *App) syncTunStateWithRetry(m *systray.MenuItem, maxRetries int) {
	for i := 0; i < maxRetries; i++ {
		if a.fetchAndUpdateTunState(m) {
			return
		}
		time.Sleep(800 * time.Millisecond)
	}
	fmt.Println("警告：多次尝试后仍无法获取 TUN 状态（mihomo 可能尚未启动完成）")
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
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return false
	}

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

// ==================== TUN 开关 ====================
func (a *App) toggleTun(m *systray.MenuItem) {
	newEnable := !a.isTUNEnabled

	if err := a.setTun(newEnable); err != nil {
		fmt.Printf("切换 TUN 失败: %v\n", err)
		// 失败时强制刷新一次真实状态
		a.fetchAndUpdateTunState(m)
		return
	}

	a.isTUNEnabled = newEnable
	if newEnable {
		m.Check()
		fmt.Println("TUN 模式已开启（mihomo 自动创建虚拟网卡）")
	} else {
		m.Uncheck()
		fmt.Println("TUN 模式已关闭（mihomo 自动释放虚拟网卡）")
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

// ==================== 系统代理开关 ====================
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
	switch runtime.GOOS {
	case "windows":
		cmd := exec.Command("powershell", "-Command",
			`Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyServer -Value "`+proxyAddr+`"; `+
				`Set-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -Name ProxyEnable -Value 1`)
		a.hideWindow(cmd)
		_ = cmd.Run()
	}
}

func (a *App) disableSystemProxy() {
	fmt.Println("关闭系统代理")
	switch runtime.GOOS {
	case "windows":
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

// ==================== 退出处理 ====================
func (a *App) onExit() {
	if a.mihomoCmd != nil && a.mihomoCmd.Process != nil {
		fmt.Println("关闭 mihomo...")
		_ = a.mihomoCmd.Process.Kill()
		_, _ = a.mihomoCmd.Process.Wait()
	}
	a.disableSystemProxy()
	// 优雅关闭 TUN（推荐）
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
