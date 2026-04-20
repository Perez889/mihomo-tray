// shutdown.go
package main

import (
    "syscall"
    "unsafe"
)

var (
    user32               = syscall.NewLazyDLL("user32.dll")
    procRegisterClassW   = user32.NewProc("RegisterClassW")
    procCreateWindowExW  = user32.NewProc("CreateWindowExW")
    procDefWindowProcW   = user32.NewProc("DefWindowProcW")
    procGetMessageW      = user32.NewProc("GetMessageW")
    procTranslateMessage = user32.NewProc("TranslateMessage")
    procDispatchMessageW = user32.NewProc("DispatchMessageW")
)

const (
    WM_QUERYENDSESSION = 0x0011
    WM_ENDSESSION      = 0x0016
)

func (a *App) wndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
    switch msg {
    case WM_QUERYENDSESSION, WM_ENDSESSION:
        a.log("收到关机/重启/注销信号 → 自动关闭系统代理 + TUN + mihomo")
        a.disableSystemProxy()
        if a.isTUNEnabled {
            _ = a.setTun(false)
        }
        if a.mihomoCmd != nil && a.mihomoCmd.Process != nil {
            _ = a.mihomoCmd.Process.Kill()
        }
    }
    ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
    return ret
}

func (a *App) createHiddenWindow() {
    className := syscall.StringToUTF16Ptr("NetTrayHiddenWindow")

    var wc struct {
        style         uint32
        lpfnWndProc   uintptr
        cbClsExtra    int32
        cbWndExtra    int32
        hInstance     uintptr
        hIcon         uintptr
        hCursor       uintptr
        hbrBackground uintptr
        lpszMenuName  *uint16
        lpszClassName *uint16
    }

    wc.lpfnWndProc = syscall.NewCallback(a.wndProc)
    wc.lpszClassName = className

    procRegisterClassW.Call(uintptr(unsafe.Pointer(&wc)))

    procCreateWindowExW.Call(
        0,
        uintptr(unsafe.Pointer(className)),
        0,
        0, 0, 0, 0,
        0, 0, 0, 0,
    )
}

func (a *App) messageLoop() {
    var msg struct {
        hwnd    uintptr
        message uint32
        wParam  uintptr
        lParam  uintptr
        time    uint32
        pt      struct{ x, y int32 }
    }

    for {
        ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
        if ret == 0 {
            break
        }
        procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
        procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
    }
}
