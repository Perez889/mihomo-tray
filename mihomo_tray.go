package main

import (
	_ "embed"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/getlantern/systray"
)

//go:embed clash.ico
var trayIcon []byte

var mihomoCmd *exec.Cmd

// ⭐ 如果你有 secret 填这里，没有就留空
var mihomoSecret = ""

// ⭐ API地址
var apiURL = "http://127.0.0.1:9090"

func main() {
	systray.Run(onReady, onExit)
}

func onReady() {
	// 托盘图标
	if runtime.GOOS == "windows" {
		systray.SetIcon(trayIcon)
	} else {
		systray.SetTemplateIcon(trayIcon, trayIcon)
	}

	systray.SetTooltip("Mihomo Proxy\n托盘管理小工具")

	// ⭐ 系统代理开关
	mSysProxy := systray.AddMenuItemCheckbox("代理", "开关系统代理", false)

	mRestart := systray.AddMenuItem("重启", "重启 Mihomo")
	mOpen := systray.AddMenuItem("面板", "打开 Zashboard 面板")
	mQuit := systray.AddMenuItem("退出", "退出程序并关闭 mihomo")

	// ⭐ 启动时同步系统代理状态
	go func() {
		time.Sleep(1 * time.Second)
		if getSystemProxy() {
			mSysProxy.Check()
		}
	}()

	go func() {
		for {
			select {

			// ⭐ 系统代理点击
			case <-mSysProxy.ClickedCh:
				newState := !mSysProxy.Checked()

				if newState {
					mSysProxy.Check()
				} else {
					mSysProxy.Uncheck()
				}

				setSystemProxy(newState)

			case <-mRestart.ClickedCh:
				restartMihomo()

			case <-mOpen.ClickedCh:
				openDashboard()

			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()

	// 启动 Mihomo
	go startMihomo()
}

//
// ⭐ 设置系统代理（核心）
//
func setSystemProxy(enable bool) {
	json := []byte(fmt.Sprintf(`{"system_proxy": %v}`, enable))

	req, _ := http.NewRequest(
		"PUT",
		apiURL+"/configs",
		bytes.NewBuffer(json),
	)

	req.Header.Set("Content-Type", "application/json")

	// ⭐ 带认证
	if mihomoSecret != "password" {
		req.Header.Set("Authorization", "Bearer "+mihomoSecret)
	}

	_, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("系统代理设置失败:", err)
		return
	}

	fmt.Println("系统代理:", enable)
}

//
// ⭐ 获取当前系统代理状态
//
func getSystemProxy() bool {
	req, _ := http.NewRequest("GET", apiURL+"/configs", nil)

	if mihomoSecret != "" {
		req.Header.Set("Authorization", "Bearer "+mihomoSecret)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	return strings.Contains(string(body), `"systemProxy":true`) ||
		strings.Contains(string(body), `"system_proxy":true`)
}

//
// ✅ 启动（带检测）
//
func startMihomo() {
	if isRunning(mihomoCmd) {
		fmt.Println("mihomo 已在运行")
		return
	}
	startMihomoForce()
}

//
// ✅ 强制启动（重启用）
//
func startMihomoForce() {
	baseDir := appDir()

	exeName := "mihomo"
	if runtime.GOOS == "windows" {
		exeName = "mihomo.exe"
	}

	exePath := filepath.Join(baseDir, exeName)

	cmd := exec.Command(exePath, "-d", ".")
	cmd.Dir = baseDir

	if runtime.GOOS == "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		fmt.Println("启动失败:", err)
		return
	}

	mihomoCmd = cmd
	fmt.Println("mihomo 启动成功")
}

//
// ✅ 重启（已适配 TUN）
//
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

//
// ✅ 等待资源释放
//
func waitForRelease() {
	time.Sleep(2 * time.Second)

	for i := 0; i < 6; i++ {
		if !isPortOpen("127.0.0.1:9090") {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
}

//
// ✅ 端口检测
//
func isPortOpen(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

//
// ✅ 判断进程是否存活
//
func isRunning(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	err := cmd.Process.Signal(syscall.Signal(0))
	return err == nil
}

//
// ✅ 打开面板
//
func openDashboard() {
	url := apiURL + "/ui/zashboard"

	switch runtime.GOOS {
	case "darwin":
		exec.Command("open", url).Start()
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		exec.Command("xdg-open", url).Start()
	}
}

//
// ✅ 退出清理
//
func onExit() {
	if mihomoCmd != nil && mihomoCmd.Process != nil {
		fmt.Println("关闭 mihomo...")
		_ = mihomoCmd.Process.Kill()
		_, _ = mihomoCmd.Process.Wait()
	}
}

//
// ✅ 获取程序目录
//
func appDir() string {
	exePath, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exePath)
}
