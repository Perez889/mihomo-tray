package main

import (
	_ "embed"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
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

const ghProxy = "https://gh-proxy.org/"

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
	mixedPort            string
	controllerAddr       string
	secret               string
	dashboardURL         string
}

func NewApp() *App {
	app := &App{
		isSystemProxyEnabled: false,
		mixedPort:            "1081",
		controllerAddr:       "127.0.0.1:9090",
		secret:               "",
	}
	app.loadConfig()
	return app
}

// ==================== 配置加载（严格从 config.yaml 获取） ====================
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
	fmt.Printf("配置加载成功 → 端口: %s | 控制器: %s | Secret: %q\n",
		a.mixedPort, a.controllerAddr, a.secret)
}

func (a *App) buildDashboardURL() {
	if a.secret != "" {
		a.dashboardURL = fmt.Sprintf("http://%s/ui/zashboard?secret=%s", a.controllerAddr, a.secret)
	} else {
		a.dashboardURL = fmt.Sprintf("http://%s/ui/zashboard", a.controllerAddr)
	}
}

// ==================== 下载最新内核（自动加 gh-proxy） ====================
func (a *App) downloadKernel() {
	exeName := "mihomo"
	if runtime.GOOS == "windows" {
		exeName = "mihomo.exe"
	}
	kernelPath := filepath.Join(a.appDir(), exeName)

	if _, err := os.Stat(kernelPath); err == nil {
		fmt.Printf("✅ 内核已存在（%s），如需重新下载请先删除该文件\n", exeName)
		return
	}

	fmt.Println("🚀 开始下载最新 mihomo 内核（已自动添加 gh-proxy 前缀）...")

	// GitHub API + gh-proxy
	apiURL := "https://api.github.com/repos/MetaCubeX/mihomo/releases/latest"
	proxyAPI := ghProxy + apiURL

	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}

	resp, err := http.Get(proxyAPI)
	if err != nil || resp.StatusCode != 200 {
		fmt.Println("gh-proxy 失败，尝试直连 GitHub...")
		resp, err = http.Get(apiURL)
	}
	if err != nil {
		fmt.Println("❌ 获取版本信息失败")
		return
	}
	defer resp.Body.Close()

	json.NewDecoder(resp.Body).Decode(&release)

	// 自动匹配当前系统架构
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	ext := "zip"
	if goos != "windows" {
		ext = "gz"
	}
	version := strings.TrimPrefix(release.TagName, "v")
	targetName := fmt.Sprintf("mihomo-%s-%s-%s.%s", goos, goarch, version, ext)

	var downloadURL string
	for _, asset := range release.Assets {
		if asset.Name == targetName {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		fmt.Printf("❌ 未找到适合 %s-%s 的内核文件\n", goos, goarch)
		return
	}

	// 给下载链接也加上 gh-proxy
	proxyDownloadURL := ghProxy + downloadURL
	fmt.Printf("📥 正在下载: %s\n", targetName)

	tmpPath := filepath.Join(os.TempDir(), targetName)
	tmpFile, _ := os.Create(tmpPath)
	defer os.Remove(tmpPath)

	dlResp, err := http.Get(proxyDownloadURL)
	if err != nil || dlResp.StatusCode != 200 {
		fmt.Println("gh-proxy 下载失败，尝试直连...")
		dlResp, _ = http.Get(downloadURL)
	}
	if dlResp == nil || dlResp.StatusCode != 200 {
		fmt.Println("❌ 下载失败")
		return
	}
	defer dlResp.Body.Close()

	io.Copy(tmpFile, dlResp.Body)
	tmpFile.Close()

	if err := a.extractKernel(tmpPath, kernelPath); err != nil {
		fmt.Println("❌ 解压失败")
		return
	}

	fmt.Printf("✅ 内核下载并安装完成！\n   路径: %s\n   版本: %s\n   点击「重启内核」使用\n", kernelPath, release.TagName)
}

func (a *App) extractKernel(tmpPath, kernelPath string) error {
	if runtime.GOOS == "windows" {
		z, err := zip.OpenReader(tmpPath)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, f := range z.File {
			if strings.HasSuffix(f.Name, ".exe") || f.Name == "mihomo" {
				rc, _ := f.Open()
				out, _ := os.Create(kernelPath)
				io.Copy(out, rc)
				rc.Close()
				out.Close()
				return nil
			}
		}
	} else {
		gzFile, _ := os.Open(tmpPath)
		gr, _ := gzip.NewReader(gzFile)
		out, _ := os.Create(kernelPath)
		io.Copy(out, gr)
		gr.Close()
		gzFile.Close()
		out.Close()
		os.Chmod(kernelPath, 0755)
	}
	return nil
}

// ==================== UI 初始化（新增下载菜单） ====================
func (a *App) onReady() {
	if runtime.GOOS == "windows" {
		systray.SetIcon(trayIcon)
	} else {
		systray.SetTemplateIcon(trayIcon, trayIcon)
	}
	systray.SetTooltip("Mihomo Proxy\n托盘工具")

	mRestart := systray.AddMenuItem("重启内核", "重启 Mihomo")
	systray.AddSeparator()
	mOpen := systray.AddMenuItem("打开面板", "打开 Zashboard")
	systray.AddSeparator()
	mProxy := systray.AddMenuItemCheckbox("系统代理", "点击切换系统代理开关", false)
	systray.AddSeparator()
	mDownload := systray.AddMenuItem("内核下载", "自动加 gh-proxy 下载 mihomo")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出应用", "退出并关闭 mihomo")

	a.updateProxyMenu(mProxy)

	go func() {
		for {
			select {
			case <-mRestart.ClickedCh:
				a.restartMihomo()
			case <-mDownload.ClickedCh:
				go a.downloadKernel()
			case <-mOpen.ClickedCh:
				a.openDashboard()
			case <-mProxy.ClickedCh:
				a.toggleSystemProxy(mProxy)
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()

	go a.startMihomo()
}

func (a *App) updateProxyMenu(m *systray.MenuItem) {
	if a.isSystemProxyEnabled {
		m.Check()
	} else {
		m.Uncheck()
	}
}

// ==================== 以下为你原来的所有函数（未改动） ====================
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
	case "darwin":
		exec.Command("networksetup", "-setwebproxy", "Wi-Fi", "127.0.0.1", a.mixedPort).Start()
		exec.Command("networksetup", "-setsecurewebproxy", "Wi-Fi", "127.0.0.1", a.mixedPort).Start()
		exec.Command("networksetup", "-setsocksfirewallproxy", "Wi-Fi", "127.0.0.1", a.mixedPort).Start()
	default:
		exec.Command("gsettings", "set", "org.gnome.system.proxy", "mode", "manual").Run()
		exec.Command("gsettings", "set", "org.gnome.system.proxy.http", "host", "127.0.0.1").Run()
		exec.Command("gsettings", "set", "org.gnome.system.proxy.http", "port", a.mixedPort).Run()
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
	case "darwin":
		exec.Command("networksetup", "-setwebproxystate", "Wi-Fi", "off").Start()
		exec.Command("networksetup", "-setsecurewebproxystate", "Wi-Fi", "off").Start()
		exec.Command("networksetup", "-setsocksfirewallproxystate", "Wi-Fi", "off").Start()
	default:
		exec.Command("gsettings", "set", "org.gnome.system.proxy", "mode", "none").Run()
	}
}

func (a *App) hideWindow(cmd *exec.Cmd) {
	if runtime.GOOS == "windows" && cmd != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow: true,
			CreationFlags: 0x08000000,
		}
	}
}

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
