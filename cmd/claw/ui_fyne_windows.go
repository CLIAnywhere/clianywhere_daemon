//go:build windows

package main

import (
	"bytes"
	"fmt"
	"image"
	_ "image/png"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/lxn/win"

	qrcode "github.com/skip2/go-qrcode"
)

const (
	wsOverlappedWindow = 0x00CF0000
	wsVisible          = 0x10000000
	wsChild            = 0x40000000
	ssBitmap           = 0x0000000E
	stmSetImage        = 0x0172
	lrLoadFromFile     = 0x0010
	lrCreateDIBSection = 0x2000
	wmDestroy          = 0x0002
	wmUser             = 0x0400 // custom message: close window from goroutine
	cwUseDefault       = 0x80000000
	msgboxYes          = 6
	IMAGE_BITMAP       = 0
)

var (
	modkernel32    = syscall.NewLazyDLL("kernel32.dll")
	moduser32      = syscall.NewLazyDLL("user32.dll")
	pGetModuleW    = modkernel32.NewProc("GetModuleHandleW")
	pCreateWinExW  = moduser32.NewProc("CreateWindowExW")
	pDefWndProcW   = moduser32.NewProc("DefWindowProcW")
	pPostQuitMsg   = moduser32.NewProc("PostQuitMessage")
	pLoadImageW    = moduser32.NewProc("LoadImageW")
	pSendMessageW  = moduser32.NewProc("SendMessageW")
	pGetMessageW   = moduser32.NewProc("GetMessageW")
	pTranslateMsg  = moduser32.NewProc("TranslateMessage")
	pDispatchMsgW  = moduser32.NewProc("DispatchMessageW")
	pSetWindowText = moduser32.NewProc("SetWindowTextW")
	pPostMsg              = moduser32.NewProc("PostMessageW")
	pAdjustWindowRectEx   = moduser32.NewProc("AdjustWindowRectEx")
)

// qrBindModeUI Windows: show QR code in native Win32 window, confirm via MessageBox
func qrBindModeUI(cfg *Config) (retAK string, retErr error) {
	defer func() {
		if r := recover(); r != nil {
			showWin32Error("CliAnyWhere", fmt.Sprintf("QR bind panic: %v", r))
			retAK = ""
			retErr = fmt.Errorf("panic: %v", r)
		}
	}()

	result, err := GenerateBindCode(cfg)
	if err != nil {
		showWin32Error("CliAnyWhere", fmt.Sprintf("Failed to generate bindcode: %v", err))
		return "", err
	}

	qr, err := qrcode.New(string(result.QRPayload), qrcode.Low)
	if err != nil {
		return "", fmt.Errorf("failed to generate QR code: %w", err)
	}
	qr.DisableBorder = false

	pngBytes, err := qr.PNG(256)
	if err != nil {
		return "", fmt.Errorf("failed to generate PNG: %w", err)
	}

	img, _, err := image.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return "", fmt.Errorf("failed to decode QR: %w", err)
	}

	bmpPath, err := saveTempBMP(img)
	if err != nil {
		return "", fmt.Errorf("failed to save temp BMP: %w", err)
	}
	defer os.Remove(bmpPath)

	return showQRWin32Window(result, bmpPath)
}

// showWin32Error shows an error message via Win32 MessageBox (no console needed)
func showWin32Error(title, message string) {
	caption, _ := syscall.UTF16PtrFromString(title)
	text, _ := syscall.UTF16PtrFromString(message)
	moduser32.NewProc("MessageBoxW").Call(
		0,
		uintptr(unsafe.Pointer(text)),
		uintptr(unsafe.Pointer(caption)),
		0x00000010, // MB_ICONERROR
	)
}

func saveTempBMP(img image.Image) (string, error) {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	rowSize := (w*3 + 3) &^ 3
	fileSize := 14 + 40 + rowSize*h
	bmp := make([]byte, fileSize)

	bmp[0], bmp[1] = 'B', 'M'
	putU32(bmp[2:6], uint32(fileSize))
	putU32(bmp[10:14], 54)
	putU32(bmp[14:18], 40)
	putU32(bmp[18:22], uint32(w))
	putU32(bmp[22:26], uint32(h))
	putU16(bmp[26:28], 1)
	putU16(bmp[28:30], 24)

	for y := 0; y < h; y++ {
		srcY := bounds.Max.Y - 1 - y
		off := 54 + y*rowSize
		for x := 0; x < w; x++ {
			r, g, b, _ := img.At(bounds.Min.X+x, srcY).RGBA()
			bmp[off+x*3+0] = byte(b >> 8)
			bmp[off+x*3+1] = byte(g >> 8)
			bmp[off+x*3+2] = byte(r >> 8)
		}
	}

	path := filepath.Join(os.TempDir(), "clianywhere_qr.bmp")
	return path, os.WriteFile(path, bmp, 0644)
}

func putU32(b []byte, v uint32) { b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24) }
func putU16(b []byte, v uint16) { b[0], b[1] = byte(v), byte(v>>8) }

func showQRWin32Window(result *BindCodeResult, bmpPath string) (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	className, _ := syscall.UTF16PtrFromString("CliAnyWhereQR")
	title, _ := syscall.UTF16PtrFromString("CliAnyWhere - Scan to Bind")

	hInst, _, _ := pGetModuleW.Call(0)

	// register window class (WNDCLASSEX)
	wc := win.WNDCLASSEX{
		CbSize:        uint32(unsafe.Sizeof(win.WNDCLASSEX{})),
		LpfnWndProc:   syscall.NewCallback(qrWndProc),
		HInstance:     win.HINSTANCE(hInst),
		HbrBackground: win.HBRUSH(6 + 1),
		LpszClassName: className,
	}
	win.RegisterClassEx(&wc)

	// calculate window size: client area = bitmap + padding
	const bmpSize = 256
	const pad = 8
	clientW := bmpSize + pad*2
	clientH := bmpSize + pad*2

	// adjust for non-client area (title bar, borders)
	var rc win.RECT
	rc.Right = int32(clientW)
	rc.Bottom = int32(clientH)
	pAdjustWindowRectEx.Call(
		uintptr(unsafe.Pointer(&rc)),
		wsOverlappedWindow, 0, 0)
	winW := int(rc.Right - rc.Left)
	winH := int(rc.Bottom - rc.Top)

	// create main window
	hwnd, _, _ := pCreateWinExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow|wsVisible,
		cwUseDefault, cwUseDefault, uintptr(winW), uintptr(winH),
		0, 0, uintptr(hInst), 0)
	if hwnd == 0 {
		return "", fmt.Errorf("CreateWindowEx failed")
	}

	// load BMP
	bmpPathPtr, _ := syscall.UTF16PtrFromString(bmpPath)
	hBmp, _, _ := pLoadImageW.Call(
		0, uintptr(unsafe.Pointer(bmpPathPtr)),
		IMAGE_BITMAP, bmpSize, bmpSize,
		lrLoadFromFile|lrCreateDIBSection)
	if hBmp == 0 {
		return "", fmt.Errorf("LoadImage failed")
	}

	// get actual client area and center the static control
	var client win.RECT
	win.GetClientRect(win.HWND(hwnd), &client)
	cx := (int(client.Right) - bmpSize) / 2
	cy := (int(client.Bottom) - bmpSize) / 2

	staticClass, _ := syscall.UTF16PtrFromString("STATIC")
	hStatic, _, _ := pCreateWinExW.Call(
		0, uintptr(unsafe.Pointer(staticClass)), 0,
		wsChild|wsVisible|ssBitmap,
		uintptr(cx), uintptr(cy), bmpSize, bmpSize,
		hwnd, 0, uintptr(hInst), 0)
	pSendMessageW.Call(hStatic, stmSetImage, IMAGE_BITMAP, hBmp)

	// center on screen
	sw := win.GetSystemMetrics(win.SM_CXSCREEN)
	sh := win.GetSystemMetrics(win.SM_CYSCREEN)
	win.MoveWindow(win.HWND(hwnd), (sw-int32(winW))/2, (sh-int32(winH))/2, int32(winW), int32(winH), true)

	// WebSocket goroutine
	var bindResult string
	var bindErr error
	done := make(chan struct{})
	alive := true

	go func() {
		conn, err := ConnectAndSendBindCode(result.TSWSURL, result.BindCode)
		if err != nil {
			qrSetText(hwnd, fmt.Sprintf("Connect failed: %v", err))
			return
		}
		defer conn.Close()

		qrSetText(hwnd, "Scan QR code with app...")

		accesskey, deviceName, err := WaitForBindResponse(conn, 120*time.Second)
		if err != nil {
			if alive {
				qrSetText(hwnd, fmt.Sprintf("Binding failed: %v", err))
			}
			return
		}

		if alive {
			caption, _ := syscall.UTF16PtrFromString("Confirm Binding")
			text, _ := syscall.UTF16PtrFromString(
				fmt.Sprintf("Bind AccessKey from \"%s\"?\n\nIf this is NOT your own AccessKey, your device could be controlled by others.", deviceName))
			ret, _, _ := moduser32.NewProc("MessageBoxW").Call(
				hwnd, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(caption)),
				0x00000024) // MB_YESNO | MB_ICONWARNING
			if ret == msgboxYes {
				bindResult = accesskey
			} else {
				bindErr = fmt.Errorf("binding cancelled")
			}
		}
		close(done)
		// notify main thread to close window (DestroyWindow must be called from the thread that owns the window)
		pPostMsg.Call(hwnd, wmUser, 0, 0)
	}()

	// message loop
	var msg win.MSG
	for {
		ret, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if ret == 0 {
			break
		}
		if msg.Message == wmUser {
			// goroutine requests window close
			win.DestroyWindow(win.HWND(hwnd))
			continue
		}
		pTranslateMsg.Call(uintptr(unsafe.Pointer(&msg)))
		pDispatchMsgW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	alive = false

	select {
	case <-done:
	default:
		bindErr = fmt.Errorf("window closed")
	}

	if bindErr != nil {
		return "", bindErr
	}
	return bindResult, nil
}

func qrSetText(hwnd uintptr, text string) {
	child := win.GetWindow(win.HWND(hwnd), win.GW_CHILD)
	if child != 0 {
		t, _ := syscall.UTF16PtrFromString(text)
		pSetWindowText.Call(uintptr(child), uintptr(unsafe.Pointer(t)))
	}
}

func qrWndProc(hwnd win.HWND, msg uint32, wparam, lparam uintptr) uintptr {
	if msg == wmDestroy {
		pPostQuitMsg.Call(0)
		return 0
	}
	r, _, _ := pDefWndProcW.Call(uintptr(hwnd), uintptr(msg), wparam, lparam)
	return r
}
