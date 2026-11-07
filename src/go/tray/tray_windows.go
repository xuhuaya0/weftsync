//go:build windows

package tray

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// The Win32 surface this file needs.
var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")

	procRegisterClassEx     = user32.NewProc("RegisterClassExW")
	procUnregisterClass     = user32.NewProc("UnregisterClassW")
	procCreateWindowEx      = user32.NewProc("CreateWindowExW")
	procDefWindowProc       = user32.NewProc("DefWindowProcW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procGetMessage          = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessage     = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procPostMessage         = user32.NewProc("PostMessageW")
	procRegisterMessage     = user32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenu          = user32.NewProc("AppendMenuW")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procSetMenuDefaultItem  = user32.NewProc("SetMenuDefaultItem")
	procDestroyIcon         = user32.NewProc("DestroyIcon")
	procCreateIconIndirect  = user32.NewProc("CreateIconIndirect")
	procShellNotifyIcon     = shell32.NewProc("Shell_NotifyIconW")
	procShellExecute        = shell32.NewProc("ShellExecuteW")
	procGetModuleHandle     = kernel32.NewProc("GetModuleHandleW")

	// These live in gdi32, not user32. Asking the wrong DLL panics at Call
	// time rather than returning an error, which is how the first version
	// killed the whole process instead of just failing to draw.
	procCreateDIBSection = gdi32.NewProc("CreateDIBSection")
	procCreateBitmap     = gdi32.NewProc("CreateBitmap")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
)

const (
	wmDestroy        = 0x0002
	wmClose          = 0x0010
	wmCommand        = 0x0111
	wmRButtonUp      = 0x0205
	wmLButtonDblClk  = 0x0203
	wmContextMenu    = 0x007B
	wmApp            = 0x8000
	callbackMessage  = wmApp + 1
	trayIconID       = 1
	menuOpenConsole  = 1
	menuRunAll       = 2
	menuQuit         = 3
	mfString         = 0x0000
	mfSeparator      = 0x0800
	mfDefault        = 0x1000
	tpmRightButton   = 0x0002
	swShowNormal     = 1
	iconSize         = 32
	biRGB            = 0
	dibRGBColors     = 0
	idiApplication   = 32512
	notificationInfo = 0x10
	notifyMessage    = 0x00000001
	notifyIcon       = 0x00000002
	notifyTip        = 0x00000004
)

// notifyIconData is NOTIFYICONDATAW at the Vista-or-later size. cbSize carries
// the struct size, and Windows uses it to decide which fields it may read, so
// the layout has to match the struct exactly.
type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     uintptr
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type point struct{ X, Y int32 }

type msgStruct struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type iconInfo struct {
	FIcon    int32
	XHotspot uint32
	YHotspot uint32
	HbmMask  uintptr
	HbmColor uintptr
}

type bitmapV5Header struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
	RedMask       uint32
	GreenMask     uint32
	BlueMask      uint32
	AlphaMask     uint32
	CSType        uint32
	Endpoints     [36]byte
	GammaRed      uint32
	GammaGreen    uint32
	GammaBlue     uint32
	Intent        uint32
	ProfileData   uint32
	ProfileSize   uint32
	Reserved      uint32
}

// Tray is a live notification-area icon. The zero value is not usable; obtain
// one from Start and call Stop when done.
type Tray struct {
	cfg Config

	// hwnd is written on the message thread before Start returns, and read by
	// Stop from another goroutine.
	mu        sync.Mutex
	hwnd      uintptr
	hIcon     uintptr
	className *uint16
	ready     chan error
	done      chan struct{}
	closed    sync.Once
	quit      chan struct{}
}

// traySeq numbers window classes. The class name has to be unique per instance:
// a window class carries the WndProc, so two trays sharing a name would send the
// second icon's messages to the first tray's handler (and the class registration
// would fail outright on the second Start).
var traySeq atomic.Uint32

// Start creates the icon and begins pumping messages.
//
// Messages must be pumped on the thread that created the window, so the work
// happens on a dedicated locked OS thread. Start returns once the icon exists
// (or once the attempt failed), which is what makes a broken tray harmless: the
// caller sees the error and carries on.
func Start(cfg Config) (*Tray, error) {
	t := &Tray{
		cfg:   cfg,
		ready: make(chan error, 1),
		done:  make(chan struct{}),
		quit:  make(chan struct{}),
	}
	go t.run()
	if err := <-t.ready; err != nil {
		return nil, err
	}
	return t, nil
}

// Stop removes the icon and ends the message loop. It is safe to call twice.
func (t *Tray) Stop() {
	t.closed.Do(func() {
		close(t.quit)

		t.mu.Lock()
		hwnd := t.hwnd
		t.mu.Unlock()

		if hwnd != 0 {
			// DestroyWindow must run on the owning thread, so ask it to.
			procPostMessage.Call(hwnd, wmClose, 0, 0)
		}
		<-t.done

		if hIcon := t.icon(); hIcon != 0 {
			procDestroyIcon.Call(hIcon)
		}
	})
}

func (t *Tray) icon() uintptr {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.hIcon
}

// Notify shows a balloon tip. Failures are reported but never fatal: a
// notification is a courtesy, not part of the sync.
func (t *Tray) Notify(title, text string) error {
	t.mu.Lock()
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd == 0 {
		return errors.New("tray: not running")
	}

	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = trayIconID
	nid.UFlags = notificationInfo
	copyUTF16(nid.SzInfoTitle[:], title)
	copyUTF16(nid.SzInfo[:], text)
	nid.DwInfoFlags = notifyMessage

	ok, _, err := procShellNotifyIcon.Call(notifyMessage+1, /* NIM_MODIFY */
		uintptr(unsafe.Pointer(&nid)))
	if ok == 0 {
		return fmt.Errorf("tray: modify icon: %v", err)
	}
	return nil
}

// run owns the window for its whole life on one OS thread.
func (t *Tray) run() {
	// A window belongs to the thread that created it and its messages are
	// dispatched on that thread, so this must not migrate.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(t.done)

	// A panic on this goroutine would take the daemon down with it. The tray is
	// a shortcut to the web UI, so losing it must cost nothing but the icon.
	sent := false
	report := func(err error) {
		if sent {
			return
		}
		sent = true
		t.ready <- err
	}
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("tray: %v", r)
			report(err)
			if sent && t.cfg.OnError != nil {
				t.cfg.OnError(err)
			}
		}
	}()

	className, err := syscall.UTF16PtrFromString(
		fmt.Sprintf("weftsyncTrayWindow#%d", traySeq.Add(1)))
	if err != nil {
		report(err)
		return
	}
	t.mu.Lock()
	t.className = className
	t.mu.Unlock()

	hInst, _, _ := procGetModuleHandle.Call(0)
	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   syscall.NewCallback(t.wndProc),
		HInstance:     hInst,
		HIcon:         0,
		LpszClassName: className,
	}
	if atom, _, callErr := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		report(fmt.Errorf("tray: RegisterClassEx: %v", callErr))
		return
	}

	// A plain hidden window: it never shows, it only receives the icon's
	// callback message and the menu commands.
	hwnd, _, callErr := procCreateWindowEx.Call(
		0, uintptr(unsafe.Pointer(className)), 0, 0,
		0, 0, 0, 0, 0, 0, hInst, 0)
	if hwnd == 0 {
		report(fmt.Errorf("tray: CreateWindowEx: %v", callErr))
		return
	}

	hIcon, err := makeIcon()
	if err != nil {
		procDestroyWindow.Call(hwnd)
		report(err)
		return
	}

	nid := notifyIconData{
		CbSize:           uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd:             hwnd,
		UID:              trayIconID,
		UFlags:           notifyMessage | notifyIcon | notifyTip,
		UCallbackMessage: callbackMessage,
		HIcon:            hIcon,
	}
	copyUTF16(nid.SzTip[:], t.cfg.Tooltip)

	if ok, _, callErr := procShellNotifyIcon.Call(0, /* NIM_ADD */
		uintptr(unsafe.Pointer(&nid))); ok == 0 {
		procDestroyIcon.Call(hIcon)
		procDestroyWindow.Call(hwnd)
		report(fmt.Errorf("tray: Shell_NotifyIcon: %v", callErr))
		return
	}

	t.mu.Lock()
	t.hwnd = hwnd
	t.hIcon = hIcon
	t.mu.Unlock()

	// Tell the caller we are live only after the icon is really there.
	report(nil)

	t.pump()
}

// pump runs the message loop until Stop asks the window to close.
func (t *Tray) pump() {
	var m msgStruct
	for {
		ret, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		// 0 means WM_QUIT; -1 means an error, and spinning on an error would
		// burn a core forever.
		if int32(ret) == 0 || int32(ret) == -1 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// wndProc handles the messages the icon generates.
func (t *Tray) wndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmCommand:
		switch uint32(wParam & 0xffff) {
		case menuOpenConsole:
			t.openConsole()
		case menuRunAll:
			if t.cfg.OnRunAll != nil {
				t.cfg.OnRunAll()
			}
		case menuQuit:
			if t.cfg.OnQuit != nil {
				t.cfg.OnQuit()
			}
		}
		return 0

	case callbackMessage:
		switch uint32(lParam & 0xffff) {
		case wmLButtonDblClk:
			t.openConsole()
		case wmRButtonUp, wmContextMenu:
			t.showMenu(hwnd)
		}
		return 0

	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0

	case wmDestroy:
		// Cleanup hangs off wmDestroy rather than wmClose: the window can also be
		// destroyed by something outside this package, and that path would
		// otherwise leave a ghost icon in the notification area until the mouse
		// happens to pass over it.
		var nid notifyIconData
		nid.CbSize = uint32(unsafe.Sizeof(nid))
		nid.HWnd = hwnd
		nid.UID = trayIconID
		procShellNotifyIcon.Call(2 /* NIM_DELETE */, uintptr(unsafe.Pointer(&nid)))

		// Unregister the class too, otherwise every Start/Stop cycle leaves one
		// registered class behind for the life of the process.
		t.mu.Lock()
		cls := t.className
		hInst, _, _ := procGetModuleHandle.Call(0)
		t.mu.Unlock()
		if cls != nil {
			procUnregisterClass.Call(uintptr(unsafe.Pointer(cls)), hInst)
		}
		procPostQuitMessage.Call(0)
		return 0
	}

	ret, _, _ := procDefWindowProc.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

// openConsole puts the web UI in front of the user.
func (t *Tray) openConsole() {
	if t.cfg.ConsoleURL == "" {
		return
	}
	verb, _ := syscall.UTF16PtrFromString("open")
	url, err := syscall.UTF16PtrFromString(t.cfg.ConsoleURL)
	if err != nil {
		return
	}
	procShellExecute.Call(0, uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(url)), 0, 0, swShowNormal)
}

// showMenu pops the context menu at the cursor.
func (t *Tray) showMenu(hwnd uintptr) {
	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer func() { procDestroyMenu.Call(hMenu) }()

	appendItem(hMenu, mfString, menuOpenConsole, "打开控制台")
	appendItem(hMenu, mfString, menuRunAll, "立即运行全部")
	procAppendMenu.Call(hMenu, mfSeparator, 0, 0)
	appendItem(hMenu, mfString, menuQuit, "退出")

	// Enter is the same as "open the console": the most common action.
	procSetMenuDefaultItem.Call(hMenu, menuOpenConsole, 0)

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))

	// The foreground dance is required for the menu to close when the user
	// clicks elsewhere; without it the popup can stay stuck on screen.
	procSetForegroundWindow.Call(hwnd)
	procTrackPopupMenu.Call(hMenu, tpmRightButton, uintptr(pt.X), uintptr(pt.Y), 0,
		hwnd, 0)
	procPostMessage.Call(hwnd, 0 /* WM_NULL */, 0, 0)
}

func appendItem(hMenu uintptr, flags, id uintptr, text string) {
	p, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	procAppendMenu.Call(hMenu, flags, id, uintptr(unsafe.Pointer(p)))
}

// copyUTF16 fills a fixed-size UTF-16 buffer, always leaving a terminator.
func copyUTF16(dst []uint16, s string) {
	if len(dst) == 0 {
		return
	}
	src, err := syscall.UTF16FromString(s)
	if err != nil {
		return
	}
	n := copy(dst[:len(dst)-1], src)
	if n > 0 && dst[n-1] == 0 {
		n--
	}
	dst[n] = 0
}

// makeIcon draws the icon with pixels instead of shipping an .ico file, so the
// binary stays a single file. It is a filled blue disc with a white arrow, which
// reads clearly at 16x16.
func makeIcon() (uintptr, error) {
	const size = iconSize

	var hdr bitmapV5Header
	hdr.Size = uint32(unsafe.Sizeof(hdr))
	hdr.Width = size
	// Negative height means top-down rows, which removes the flip error that
	// comes from forgetting the DIB's bottom-up order.
	hdr.Height = -size
	hdr.Planes = 1
	hdr.BitCount = 32
	hdr.Compression = biRGB
	hdr.CSType = 0x73524742 // LCS_sRGB
	hdr.AlphaMask = 0xff000000
	hdr.RedMask = 0x00ff0000
	hdr.GreenMask = 0x0000ff00
	hdr.BlueMask = 0x000000ff

	var bits unsafe.Pointer
	hbmColor, _, callErr := procCreateDIBSection.Call(0,
		uintptr(unsafe.Pointer(&hdr)), dibRGBColors,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hbmColor == 0 || bits == nil {
		return 0, fmt.Errorf("tray: CreateDIBSection: %v", callErr)
	}
	defer func() { procDeleteObject.Call(hbmColor) }()

	// BGRA, little endian, top-down.
	pix := unsafe.Slice((*byte)(bits), size*size*4)
	fillIconPixels(pix, size)

	// The AND mask must exist even with a real alpha channel; all zero means
	// "use the alpha channel".
	hbmMask, _, _ := procCreateBitmap.Call(size, size, 1, 1, 0)
	if hbmMask == 0 {
		return 0, errors.New("tray: CreateBitmap failed")
	}
	defer func() { procDeleteObject.Call(hbmMask) }()

	ii := iconInfo{FIcon: 1, HbmMask: hbmMask, HbmColor: hbmColor}
	hIcon, _, callErr := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	if hIcon == 0 {
		return 0, fmt.Errorf("tray: CreateIconIndirect: %v", callErr)
	}
	return hIcon, nil
}

// fillIconPixels paints a BGRA, top-down 32bpp buffer with the tray glyph: a
// blue disc and a white arrow pointing right.
//
// It is split out from makeIcon so the drawing can be checked without a screen.
// Either a wrong channel order or a bottom-up buffer produces an icon that is
// technically valid and visibly wrong, which is not something a "Start returned
// nil" assertion would ever catch.
func fillIconPixels(pix []byte, size int) {
	const (
		blue  = 0xeb
		green = 0x6f
		red   = 0x2f
	)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			i := (y*size + x) * 4
			if i+3 >= len(pix) {
				return
			}
			// Disc with a deliberately hard edge; anti-aliasing here would need
			// coverage math for a benefit nobody can see at 16px.
			dx, dy := x-size/2, y-size/2
			if dx*dx+dy*dy > (size/2-1)*(size/2-1) {
				continue // leave fully transparent
			}
			pix[i+0] = blue
			pix[i+1] = green
			pix[i+2] = red
			pix[i+3] = 0xff

			if arrowAt(x, y, size) {
				pix[i+0], pix[i+1], pix[i+2] = 0xff, 0xff, 0xff
			}
		}
	}
}

// arrowAt reports whether (x,y) is part of the white arrow glyph.
func arrowAt(x, y, size int) bool {
	mid := size / 2
	if x < 9 || x > size-10 {
		return false
	}
	// Shaft.
	if y >= mid-1 && y <= mid+1 {
		return true
	}
	// Head: a triangle widening towards the right edge.
	if x >= size-16 && x <= size-10 {
		if y >= mid && y-mid <= (x-(size-16)) {
			return true
		}
		if y < mid && mid-y <= (x-(size-16)) {
			return true
		}
	}
	return false
}
