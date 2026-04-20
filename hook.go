// hook.go
package main

import (
    "syscall"
    "unsafe"

    "golang.org/x/sys/windows"
)

var (
    user32              = syscall.NewLazyDLL("user32.dll")
    procSetWindowsHook  = user32.NewProc("SetWindowsHookExW")
    procCallNextHook    = user32.NewProc("CallNextHookEx")
)

const (
    WH_CALLWNDPROC     = 4
    WM_QUERYENDSESSION = 0x0011
    WM_ENDSESSION      = 0x0016
)

var hookHandle uintptr

// Windows 消息结构体
type CWPSTRUCT struct {
    lParam  uintptr
    wParam  uintptr
    message uint32
    hwnd    uintptr
}

func (a *App) installShutdownHook() {
    tid := windows.GetCurrentThreadId()

    hookHandle, _, _ = procSetWindowsHook.Call(
        WH_CALLWNDPROC,
        syscall.NewCallback(a.hookProc),
        0,
        uintptr(tid),
    )
}

func (a *App) hookProc(nCode int, wParam uintptr, lParam uintptr) uintptr {
    if nCode >= 0 {
        msg := (*CWPSTRUCT)(unsafe.Pointer(lParam))
        switch msg.message {
        case WM_QUERYENDSESSION, WM_ENDSESSION:
            a.log("收到系统关机/重启/注销信号 → 自动关闭系统代理")
            a.disableSystemProxy()
            if a.isTUNEnabled {
                _ = a.setTun(false)
            }
            if a.mihomoCmd != nil && a.mihomoCmd.Process != nil {
                _ = a.mihomoCmd.Process.Kill()
            }
        }
    }
    ret, _, _ := procCallNextHook.Call(0, uintptr(nCode), wParam, lParam)
    return ret
}
