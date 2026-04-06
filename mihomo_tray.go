package main

import (
	_ "embed"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	"golang.org/x/sys/windows"
	"gopkg.in/yaml.v3"
)

// ==================== 版本号（打包时注入） ====================
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

// ==================== 配置加载（保持不变） ====================
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

// ==================== providers gh-proxy（保持你的原有逻辑） ====================
const ghProxyPrefix = "https://gh-proxy.org/"

func (a *App) patchConfigForGHProxy() string {
	baseDir := a.appDir()
	originalPath := filepath.Join(baseDir, "config.yaml")
	patchedPath := filepath.Join(baseDir, "config.patched.yaml")

	data, err := os.ReadFile(originalPath)
	if err != nil {
		return originalPath
	}

	var cfg map[string]interface{}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return originalPath
	}

	patched := false
	for _, section := range []string{"proxy-providers", "rule-providers"} {
		if providers, ok := cfg[section].(map[string]interface{}); ok {
			for name, p := range providers {
				if pm, ok := p.(map[string]interface{}); ok {
					if url, ok := pm["url"].(string); ok && url != "" {
						trimmed := strings.TrimSpace(url)
						if isGitHubURL(trimmed) && !strings.Contains(trimmed, "gh-proxy.org") {
							pm["url"] = ghProxyPrefix + trimmed
							patched = true
							fmt.Printf("已为 %s 添加 gh-proxy: %s\n", name, section)
						}
					}
				}
			}
		}
	}

	if !patched {
		return originalPath
	}

	newData, _ := yaml.Marshal(cfg)
	if err := os.WriteFile(patchedPath, newData, 0644); err == nil {
		fmt.Println("已生成 patched 配置（自动添加 gh-proxy.org）")
		return patchedPath
	}
	return originalPath
}

func isGitHubURL(url string) bool {
	return strings.Contains(url, "github.com") ||
		strings.Contains(url, "raw.githubusercontent.com") ||
		strings.Contains(url, "gist.githubusercontent.com")
}

// ==================== 内核下载（核心修复：可靠前置代理 + fallback） ====================
func (a *App) downloadKernel() {
	exeName := "mihomo"
	if runtime.GOOS == "windows" {
		exeName = "mihomo.exe"
	}
	kernelPath := filepath.Join(a.appDir(), exeName)

	if _, err := os.Stat(kernelPath); err == nil {
		fmt.Printf("✅ 内核已存在（%s），如需重新下载请先手动删除该文件\n", exeName)
		return
	}

	fmt.Println("🚀 开始下载最新 mihomo 内核（使用 gh-proxy 前置加速）...")

	// 优先使用 gh-proxy 访问 GitHub API
	apiURL := "https://api.github.com/repos/MetaCubeX/mihomo/releases/latest"
	proxyAPI := ghProxyPrefix + strings.TrimPrefix(apiURL, "https://")

	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}

	resp, err := http.Get(proxyAPI)
	if err != nil || resp.StatusCode != http.StatusOK {
		fmt.Println("gh-proxy API 失败，尝试直连 GitHub...")
		resp, err = http.Get(apiURL)
	}
	if err != nil {
		fmt.Println("❌ 获取最新版本失败，请检查网络或稍后重试")
		return
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		fmt.Println("❌ 解析版本信息失败", err)
		return
	}

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

	// 使用 gh-proxy 下载（带 fallback）
	proxyDownloadURL := ghProxyPrefix + strings.TrimPrefix(downloadURL, "https://")
	fmt.Printf("📥 正在下载: %s\n", targetName)

	tmpPath := filepath.Join(os.TempDir(), targetName)
	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		fmt.Println("❌ 创建临时文件失败", err)
		return
	}
	defer os.Remove(tmpPath)

	dlResp, err := http.Get(proxyDownloadURL)
	if err != nil || dlResp.StatusCode != http.StatusOK {
		fmt.Println("gh-proxy 下载失败，尝试直连 GitHub...")
		dlResp, err = http.Get(downloadURL)
	}
	if err != nil || dlResp.StatusCode != http.StatusOK {
		fmt.Println("❌ 下载失败，请检查网络或尝试其他代理")
		return
	}
	defer dlResp.Body.Close()

	if _, err := io.Copy(tmpFile, dlResp.Body); err != nil {
		fmt.Println("❌ 保存下载文件失败", err)
		return
	}
	tmpFile.Close()

	// 解压并重命名
	if err := a.extractAndRenameKernel(tmpPath, kernelPath); err != nil {
		fmt.Println("❌ 解压失败:", err)
		return
	}

	fmt.Printf("✅ 内核下载并安装成功！\n   路径: %s\n   版本: %s\n   点击「重启内核」即可使用\n", kernelPath, release.TagName)
}

func (a *App) extractAndRenameKernel(tmpPath, kernelPath string) error {
	if runtime.GOOS == "windows" {
		// zip 解压
		zipReader, err := zip.OpenReader(tmpPath)
		if err != nil {
			return err
		}
		defer zipReader.Close()
		for _, f := range zipReader.File {
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
		// gzip 解压（macOS/Linux）
		gzFile, err := os.Open(tmpPath)
		if err != nil {
			return err
		}
		gr, err := gzip.NewReader(gzFile)
		if err != nil {
			return err
		}
		out, err := os.Create(kernelPath)
		if err != nil {
			return err
		}
		io.Copy(out, gr)
		gr.Close()
		gzFile.Close()
		out.Close()
		os.Chmod(kernelPath, 0755)
	}
	return nil
}

// ==================== UI 初始化（增加下载菜单） ====================
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
	mProxy := systray.AddMenuItemCheckbox("系统代理", "切换系统代理", false)
	systray.AddSeparator()
	mDownload := systray.AddMenuItem("内核下载", "首次运行或无内核时使用（gh-proxy 加速）")
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
	go a.checkKernelOnFirstRun()
}

func (a *App) checkKernelOnFirstRun() {
	exeName := "mihomo"
	if runtime.GOOS == "windows" {
		exeName = "mihomo.exe"
	}
	if _, err := os.Stat(filepath.Join(a.appDir(), exeName)); os.IsNotExist(err) {
		fmt.Println("⚠️  首次运行未检测到 mihomo 内核，请点击菜单 → 「下载最新内核」")
	}
}

func (a *App) updateProxyMenu(m *systray.MenuItem) {
	if a.isSystemProxyEnabled {
		m.Check()
	} else {
		m.Uncheck()
	}
}

// ==================== 系统代理、Mihomo 控制、onExit、appDir 等函数（保持你原来的代码） ====================
// 请把你提供的代码中从 toggleSystemProxy 到 appDir() 的所有函数完整复制到这里

// ==================== 主入口 ====================
func main() {
	ensureSingleInstance()
	app := NewApp()
	systray.Run(app.onReady, app.onExit)
}
