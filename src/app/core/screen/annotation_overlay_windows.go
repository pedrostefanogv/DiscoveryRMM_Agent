//go:build windows

package screen

import (
	"image"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"discovery/app/core/winui"
)

const (
	annotationOverlayClass = "DiscoveryRmmAnnotationOverlay"

	msgAnnotationRedraw = 0x8000 + 1 // WM_APP + 1
	msgAnnotationClose  = 0x8000 + 2 // WM_APP + 2

	timerAnnotationFade    = 1
	timerAnnotationTopmost = 2
	annotationFadeTickMS   = 33
	annotationTopmostMS    = 1000

	maNoActivate = 3
	swHide       = 0
	swShowNA     = 8 // SW_SHOWNOACTIVATE

	ulwAlpha   = 0x00000002
	acSrcOver  = 0x00
	acSrcAlpha = 0x01

	annoSwpNoSize        = 0x0001
	annoSwpNoMove        = 0x0002
	annoSwpNoActivate    = 0x0010
	annoSwpNoOwnerZOrder = 0x0200
)

var (
	annoUser32   = windows.NewLazySystemDLL("user32.dll")
	annoGdi32    = windows.NewLazySystemDLL("gdi32.dll")
	annoKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procAnnoRegisterClassExW     = annoUser32.NewProc("RegisterClassExW")
	procAnnoCreateWindowExW      = annoUser32.NewProc("CreateWindowExW")
	procAnnoDefWindowProcW       = annoUser32.NewProc("DefWindowProcW")
	procAnnoDestroyWindow        = annoUser32.NewProc("DestroyWindow")
	procAnnoShowWindow           = annoUser32.NewProc("ShowWindow")
	procAnnoSetWindowPos         = annoUser32.NewProc("SetWindowPos")
	procAnnoSetTimer             = annoUser32.NewProc("SetTimer")
	procAnnoKillTimer            = annoUser32.NewProc("KillTimer")
	procAnnoGetMessageW          = annoUser32.NewProc("GetMessageW")
	procAnnoTranslateMessage     = annoUser32.NewProc("TranslateMessage")
	procAnnoDispatchMessageW     = annoUser32.NewProc("DispatchMessageW")
	procAnnoPostQuitMessage      = annoUser32.NewProc("PostQuitMessage")
	procAnnoPostMessageW         = annoUser32.NewProc("PostMessageW")
	procAnnoUpdateLayeredWindow  = annoUser32.NewProc("UpdateLayeredWindow")
	procAnnoGetDC                = annoUser32.NewProc("GetDC")
	procAnnoReleaseDC            = annoUser32.NewProc("ReleaseDC")
	procAnnoCreateCompatibleDC   = annoGdi32.NewProc("CreateCompatibleDC")
	procAnnoDeleteDC             = annoGdi32.NewProc("DeleteDC")
	procAnnoCreateDIBSection     = annoGdi32.NewProc("CreateDIBSection")
	procAnnoSelectObject         = annoGdi32.NewProc("SelectObject")
	procAnnoDeleteObject         = annoGdi32.NewProc("DeleteObject")
	procAnnoGetModuleHandleW     = annoKernel32.NewProc("GetModuleHandleW")
)

type annoWndClassExW struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSmall  uintptr
}

type annoPoint struct {
	X int32
	Y int32
}

type annoSize struct {
	CX int32
	CY int32
}

type annoBlendFunction struct {
	BlendOp             byte
	BlendFlags          byte
	SourceConstantAlpha byte
	AlphaFormat         byte
}

type annoBitmapInfoHeader struct {
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
}

var (
	annoInstancesMu sync.Mutex
	annoInstances   = make(map[uintptr]*AnnotationOverlay)
	annoClassOnce   sync.Once
	annoWndProcPtr  = syscall.NewCallback(annotationWndProc)
)

// AnnotationOverlay exibe anotações vetoriais click-through sobre o monitor
// compartilhado usando janela Win32 layered com per-pixel alpha.
// Não aplica WDA_EXCLUDEFROMCAPTURE para que as anotações apareçam no vídeo
// capturado e na gravação da sessão.
type AnnotationOverlay struct {
	store *AnnotationStore

	mu             sync.Mutex
	bounds         image.Rectangle
	hwnd           uintptr
	visible        bool
	fadeIntervalMS uint32

	dibDC        uintptr
	dibBitmap    uintptr
	dibOldBitmap uintptr
	dibBits      unsafe.Pointer
	dibW         int
	dibH         int

	closed  atomic.Bool
	readyCh chan struct{}
	doneCh  chan struct{}
}

// NewAnnotationOverlay cria o overlay de anotações para a geometria informada
// e inicia sua thread Win32 dedicada em estado oculto.
func NewAnnotationOverlay(bounds image.Rectangle) *AnnotationOverlay {
	o := &AnnotationOverlay{
		store:   NewAnnotationStore(),
		bounds:  bounds,
		readyCh: make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	go o.runLoop()
	<-o.readyCh
	return o
}

// HandleCommand valida e aplica um comando JSON do subject .annotate e agenda
// redesenho imediato no overlay.
func (o *AnnotationOverlay) HandleCommand(raw []byte) error {
	if o == nil || o.closed.Load() {
		return nil
	}
	changed, err := o.store.Apply(raw, time.Now())
	if err != nil {
		return err
	}
	if changed {
		o.postRedraw()
	}
	return nil
}

// SetBounds atualiza a geometria do monitor coberto (ex.: troca de monitor) e
// limpa anotações anteriores que pertenciam à geometria antiga.
func (o *AnnotationOverlay) SetBounds(bounds image.Rectangle) {
	if o == nil || o.closed.Load() {
		return
	}
	o.mu.Lock()
	o.bounds = bounds
	o.mu.Unlock()
	o.store.Clear()
	o.postRedraw()
}

// Clear remove todas as anotações da tela e oculta o overlay.
func (o *AnnotationOverlay) Clear() {
	if o == nil || o.closed.Load() {
		return
	}
	if o.store.Clear() {
		o.postRedraw()
	}
}

// Close encerra a janela overlay e libera todos os recursos GDI/Win32. Idempotente.
func (o *AnnotationOverlay) Close() {
	if o == nil || !o.closed.CompareAndSwap(false, true) {
		return
	}
	o.mu.Lock()
	hwnd := o.hwnd
	o.mu.Unlock()
	if hwnd != 0 {
		procAnnoPostMessageW.Call(hwnd, msgAnnotationClose, 0, 0)
	}
	select {
	case <-o.doneCh:
	case <-time.After(2 * time.Second):
	}
}

func (o *AnnotationOverlay) postRedraw() {
	o.mu.Lock()
	hwnd := o.hwnd
	o.mu.Unlock()
	if hwnd != 0 && !o.closed.Load() {
		procAnnoPostMessageW.Call(hwnd, msgAnnotationRedraw, 0, 0)
	}
}

func (o *AnnotationOverlay) runLoop() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(o.doneCh)

	hInst, _, _ := procAnnoGetModuleHandleW.Call(0)
	classNamePtr, _ := windows.UTF16PtrFromString(annotationOverlayClass)

	annoClassOnce.Do(func() {
		wc := annoWndClassExW{
			Size:      uint32(unsafe.Sizeof(annoWndClassExW{})),
			WndProc:   annoWndProcPtr,
			Instance:  hInst,
			ClassName: classNamePtr,
		}
		procAnnoRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	})

	o.mu.Lock()
	b := o.bounds
	o.mu.Unlock()

	exStyle := uintptr(winui.ExStyleLayered | winui.ExStyleTransparent | winui.ExStyleTopmost | winui.ExStyleNoActivate | winui.ExStyleToolWindow)
	style := uintptr(winui.WinStylePopup)

	w := b.Dx()
	h := b.Dy()
	if w <= 0 {
		w = 1
	}
	if h <= 0 {
		h = 1
	}

	hwnd, _, _ := procAnnoCreateWindowExW.Call(
		exStyle,
		uintptr(unsafe.Pointer(classNamePtr)),
		0,
		style,
		uintptr(b.Min.X),
		uintptr(b.Min.Y),
		uintptr(w),
		uintptr(h),
		0,
		0,
		hInst,
		0,
	)

	if hwnd != 0 {
		annoInstancesMu.Lock()
		annoInstances[hwnd] = o
		annoInstancesMu.Unlock()

		o.mu.Lock()
		o.hwnd = hwnd
		o.mu.Unlock()
	}

	close(o.readyCh)

	if hwnd == 0 {
		return
	}

	var msg winui.Msg
	for {
		ret, _, _ := procAnnoGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procAnnoTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procAnnoDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}

	o.releaseDIB()
	annoInstancesMu.Lock()
	delete(annoInstances, hwnd)
	annoInstancesMu.Unlock()

	o.mu.Lock()
	o.hwnd = 0
	o.mu.Unlock()
}

func annotationWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case winui.MsgNCHitTest:
		return winui.HtTransparent
	case winui.MsgMouseActivate:
		return maNoActivate
	case msgAnnotationRedraw:
		annoInstancesMu.Lock()
		o := annoInstances[hwnd]
		annoInstancesMu.Unlock()
		if o != nil {
			o.redraw()
		}
		return 0
	case winui.MsgTimer:
		annoInstancesMu.Lock()
		o := annoInstances[hwnd]
		annoInstancesMu.Unlock()
		if o != nil {
			switch wParam {
			case timerAnnotationFade:
				o.redraw()
			case timerAnnotationTopmost:
				o.reassertTopmost()
			}
		}
		return 0
	case msgAnnotationClose:
		procAnnoKillTimer.Call(hwnd, timerAnnotationFade)
		procAnnoKillTimer.Call(hwnd, timerAnnotationTopmost)
		procAnnoDestroyWindow.Call(hwnd)
		return 0
	case winui.MsgDestroy:
		procAnnoKillTimer.Call(hwnd, timerAnnotationFade)
		procAnnoKillTimer.Call(hwnd, timerAnnotationTopmost)
		procAnnoPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procAnnoDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

func (o *AnnotationOverlay) reassertTopmost() {
	o.mu.Lock()
	hwnd := o.hwnd
	visible := o.visible
	o.mu.Unlock()
	if hwnd == 0 || !visible {
		return
	}
	hwndTopmost := ^uintptr(0) // HWND_TOPMOST = (HWND)-1
	procAnnoSetWindowPos.Call(
		hwnd,
		hwndTopmost,
		0, 0, 0, 0,
		annoSwpNoActivate|annoSwpNoMove|annoSwpNoSize|annoSwpNoOwnerZOrder,
	)
}

func (o *AnnotationOverlay) redraw() {
	o.mu.Lock()
	hwnd := o.hwnd
	bounds := o.bounds
	wasVisible := o.visible
	prevFadeMS := o.fadeIntervalMS
	o.mu.Unlock()

	if hwnd == 0 {
		return
	}

	shapes, nextWake := o.store.Snapshot(time.Now())
	if len(shapes) == 0 || bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		if wasVisible {
			procAnnoShowWindow.Call(hwnd, swHide)
			procAnnoKillTimer.Call(hwnd, timerAnnotationTopmost)
			o.mu.Lock()
			o.visible = false
			o.mu.Unlock()
		}
		if prevFadeMS != 0 {
			procAnnoKillTimer.Call(hwnd, timerAnnotationFade)
			o.mu.Lock()
			o.fadeIntervalMS = 0
			o.mu.Unlock()
		}
		return
	}

	w, h := bounds.Dx(), bounds.Dy()
	if !o.ensureDIB(w, h) {
		return
	}

	// Zera todos os pixels para ARGB = 0x00000000 (100% transparente).
	byteLen := w * h * 4
	clear(unsafe.Slice((*byte)(o.dibBits), byteLen))

	if canvas := winui.NewPARGBCanvas(o.dibBits, w, h); canvas != nil {
		for _, sh := range shapes {
			drawAnnotationShapeGDIPlus(canvas, w, h, sh)
		}
		canvas.Close()
	}

	ptDst := annoPoint{X: int32(bounds.Min.X), Y: int32(bounds.Min.Y)}
	ptSrc := annoPoint{X: 0, Y: 0}
	sz := annoSize{CX: int32(w), CY: int32(h)}
	blend := annoBlendFunction{
		BlendOp:             acSrcOver,
		BlendFlags:          0,
		SourceConstantAlpha: 255,
		AlphaFormat:         acSrcAlpha,
	}

	procAnnoUpdateLayeredWindow.Call(
		hwnd,
		0,
		uintptr(unsafe.Pointer(&ptDst)),
		uintptr(unsafe.Pointer(&sz)),
		o.dibDC,
		uintptr(unsafe.Pointer(&ptSrc)),
		0,
		uintptr(unsafe.Pointer(&blend)),
		ulwAlpha,
	)

	if !wasVisible {
		procAnnoShowWindow.Call(hwnd, swShowNA)
		procAnnoSetTimer.Call(hwnd, timerAnnotationTopmost, annotationTopmostMS, 0)
		o.mu.Lock()
		o.visible = true
		o.mu.Unlock()
		o.reassertTopmost()
	}

	var nextFadeMS uint32
	if nextWake > 0 {
		ms := uint32(nextWake.Milliseconds())
		if ms < annotationFadeTickMS {
			ms = annotationFadeTickMS
		}
		nextFadeMS = ms
	}
	if nextFadeMS == 0 && prevFadeMS != 0 {
		procAnnoKillTimer.Call(hwnd, timerAnnotationFade)
		o.mu.Lock()
		o.fadeIntervalMS = 0
		o.mu.Unlock()
	} else if nextFadeMS != 0 && nextFadeMS != prevFadeMS {
		procAnnoSetTimer.Call(hwnd, timerAnnotationFade, uintptr(nextFadeMS), 0)
		o.mu.Lock()
		o.fadeIntervalMS = nextFadeMS
		o.mu.Unlock()
	}
}

func (o *AnnotationOverlay) ensureDIB(w, h int) bool {
	if w <= 0 || h <= 0 {
		return false
	}
	if o.dibDC != 0 && o.dibW == w && o.dibH == h && o.dibBits != nil {
		return true
	}
	o.releaseDIB()

	screenDC, _, _ := procAnnoGetDC.Call(0)
	if screenDC == 0 {
		return false
	}
	defer procAnnoReleaseDC.Call(0, screenDC)

	dc, _, _ := procAnnoCreateCompatibleDC.Call(screenDC)
	if dc == 0 {
		return false
	}

	bmi := annoBitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(annoBitmapInfoHeader{})),
		Width:       int32(w),
		Height:      -int32(h), // top-down
		Planes:      1,
		BitCount:    32,
		Compression: 0, // BI_RGB
	}

	var bits unsafe.Pointer
	bmp, _, _ := procAnnoCreateDIBSection.Call(
		dc,
		uintptr(unsafe.Pointer(&bmi)),
		0, // DIB_RGB_COLORS
		uintptr(unsafe.Pointer(&bits)),
		0,
		0,
	)
	if bmp == 0 || bits == nil {
		procAnnoDeleteDC.Call(dc)
		return false
	}

	oldBmp, _, _ := procAnnoSelectObject.Call(dc, bmp)
	o.dibDC = dc
	o.dibBitmap = bmp
	o.dibOldBitmap = oldBmp
	o.dibBits = bits
	o.dibW = w
	o.dibH = h
	return true
}

func (o *AnnotationOverlay) releaseDIB() {
	if o.dibDC == 0 {
		return
	}
	if o.dibOldBitmap != 0 {
		procAnnoSelectObject.Call(o.dibDC, o.dibOldBitmap)
	}
	if o.dibBitmap != 0 {
		procAnnoDeleteObject.Call(o.dibBitmap)
	}
	procAnnoDeleteDC.Call(o.dibDC)
	o.dibDC = 0
	o.dibBitmap = 0
	o.dibOldBitmap = 0
	o.dibBits = nil
	o.dibW = 0
	o.dibH = 0
}

func drawAnnotationShapeGDIPlus(canvas *winui.PARGBCanvas, w, h int, sh AnnotationShape) {
	if canvas == nil || len(sh.Points) < 2 || sh.Alpha <= 0 {
		return
	}
	alphaByte := uint32(math.Round(clamp01(sh.Alpha) * 255))
	if alphaByte == 0 {
		return
	}
	haloAlpha := uint32(math.Round(clamp01(sh.Alpha) * 115))
	haloARGB := (haloAlpha << 24) | 0x000000
	mainARGB := (alphaByte << 24) | (sh.ColorRGB & 0x00FFFFFF)

	stroke := sh.Width
	if stroke < 1 {
		stroke = 1
	}

	switch sh.Tool {
	case AnnotationToolArrow:
		p0 := winui.PointF{X: float32(sh.Points[0].X * float64(w)), Y: float32(sh.Points[0].Y * float64(h))}
		p1 := winui.PointF{X: float32(sh.Points[len(sh.Points)-1].X * float64(w)), Y: float32(sh.Points[len(sh.Points)-1].Y * float64(h))}
		if haloAlpha > 0 {
			canvas.FillPolygons(buildArrowPolygons(p0, p1, stroke+3), haloARGB, true)
		}
		canvas.FillPolygons(buildArrowPolygons(p0, p1, stroke), mainARGB, true)

	case AnnotationToolRect:
		p0 := sh.Points[0]
		p1 := sh.Points[len(sh.Points)-1]
		x0, x1 := math.Min(p0.X, p1.X)*float64(w), math.Max(p0.X, p1.X)*float64(w)
		y0, y1 := math.Min(p0.Y, p1.Y)*float64(h), math.Max(p0.Y, p1.Y)*float64(h)
		if haloAlpha > 0 {
			canvas.FillPolygons(buildRectRingPolygons(x0, y0, x1, y1, stroke+3), haloARGB, false)
		}
		canvas.FillPolygons(buildRectRingPolygons(x0, y0, x1, y1, stroke), mainARGB, false)

	case AnnotationToolEllipse:
		p0 := sh.Points[0]
		p1 := sh.Points[len(sh.Points)-1]
		x0, x1 := math.Min(p0.X, p1.X)*float64(w), math.Max(p0.X, p1.X)*float64(w)
		y0, y1 := math.Min(p0.Y, p1.Y)*float64(h), math.Max(p0.Y, p1.Y)*float64(h)
		if haloAlpha > 0 {
			canvas.FillPolygons(buildEllipseRingPolygons(x0, y0, x1, y1, stroke+3), haloARGB, false)
		}
		canvas.FillPolygons(buildEllipseRingPolygons(x0, y0, x1, y1, stroke), mainARGB, false)

	case AnnotationToolFreehand, AnnotationToolLaser:
		pts := make([]winui.PointF, len(sh.Points))
		for i, pt := range sh.Points {
			pts[i] = winui.PointF{
				X: float32(pt.X * float64(w)),
				Y: float32(pt.Y * float64(h)),
			}
		}
		if haloAlpha > 0 {
			canvas.FillPolygons(buildPolylinePolygons(pts, stroke+3), haloARGB, true)
		}
		canvas.FillPolygons(buildPolylinePolygons(pts, stroke), mainARGB, true)
	}
}

func buildArrowPolygons(p0, p1 winui.PointF, stroke float64) [][]winui.PointF {
	dx := float64(p1.X - p0.X)
	dy := float64(p1.Y - p0.Y)
	dist := math.Hypot(dx, dy)
	if dist < 2 {
		return [][]winui.PointF{buildCirclePolygon(float64(p0.X), float64(p0.Y), stroke/2, 12)}
	}

	ux, uy := dx/dist, dy/dist
	px, py := -uy, ux

	headLen := math.Max(16, stroke*4.2)
	if headLen > dist*0.75 {
		headLen = dist * 0.75
	}
	headHalfW := math.Max(9, stroke*2.1)
	halfW := stroke / 2

	baseX := float64(p1.X) - ux*headLen
	baseY := float64(p1.Y) - uy*headLen

	shaft := []winui.PointF{
		{X: float32(float64(p0.X) + px*halfW), Y: float32(float64(p0.Y) + py*halfW)},
		{X: float32(baseX + px*halfW), Y: float32(baseY + py*halfW)},
		{X: float32(baseX - px*halfW), Y: float32(baseY - py*halfW)},
		{X: float32(float64(p0.X) - px*halfW), Y: float32(float64(p0.Y) - py*halfW)},
	}
	head := []winui.PointF{
		p1,
		{X: float32(baseX + px*headHalfW), Y: float32(baseY + py*headHalfW)},
		{X: float32(baseX - px*headHalfW), Y: float32(baseY - py*headHalfW)},
	}
	return [][]winui.PointF{shaft, head}
}

func buildRectRingPolygons(x0, y0, x1, y1, stroke float64) [][]winui.PointF {
	half := stroke / 2
	ox0, oy0, ox1, oy1 := x0-half, y0-half, x1+half, y1+half
	ix0, iy0, ix1, iy1 := x0+half, y0+half, x1-half, y1-half

	outer := []winui.PointF{
		{X: float32(ox0), Y: float32(oy0)},
		{X: float32(ox1), Y: float32(oy0)},
		{X: float32(ox1), Y: float32(oy1)},
		{X: float32(ox0), Y: float32(oy1)},
	}
	if ix1 <= ix0 || iy1 <= iy0 {
		return [][]winui.PointF{outer}
	}
	inner := []winui.PointF{
		{X: float32(ix0), Y: float32(iy0)},
		{X: float32(ix1), Y: float32(iy0)},
		{X: float32(ix1), Y: float32(iy1)},
		{X: float32(ix0), Y: float32(iy1)},
	}
	return [][]winui.PointF{outer, inner}
}

func buildEllipseRingPolygons(x0, y0, x1, y1, stroke float64) [][]winui.PointF {
	const segments = 56
	cx := (x0 + x1) / 2
	cy := (y0 + y1) / 2
	rx := math.Max(1, (x1-x0)/2)
	ry := math.Max(1, (y1-y0)/2)
	half := stroke / 2

	outer := make([]winui.PointF, segments)
	for i := 0; i < segments; i++ {
		ang := 2 * math.Pi * float64(i) / segments
		outer[i] = winui.PointF{
			X: float32(cx + (rx+half)*math.Cos(ang)),
			Y: float32(cy + (ry+half)*math.Sin(ang)),
		}
	}
	irx, iry := rx-half, ry-half
	if irx <= 1 || iry <= 1 {
		return [][]winui.PointF{outer}
	}
	inner := make([]winui.PointF, segments)
	for i := 0; i < segments; i++ {
		ang := 2 * math.Pi * float64(i) / segments
		inner[i] = winui.PointF{
			X: float32(cx + irx*math.Cos(ang)),
			Y: float32(cy + iry*math.Sin(ang)),
		}
	}
	return [][]winui.PointF{outer, inner}
}

func buildPolylinePolygons(pts []winui.PointF, stroke float64) [][]winui.PointF {
	half := stroke / 2
	polys := make([][]winui.PointF, 0, len(pts)*2)
	for i := 0; i < len(pts); i++ {
		polys = append(polys, buildCirclePolygon(float64(pts[i].X), float64(pts[i].Y), half, 10))
		if i+1 >= len(pts) {
			break
		}
		p0, p1 := pts[i], pts[i+1]
		dx := float64(p1.X - p0.X)
		dy := float64(p1.Y - p0.Y)
		dist := math.Hypot(dx, dy)
		if dist < 0.5 {
			continue
		}
		px := (-dy / dist) * half
		py := (dx / dist) * half
		polys = append(polys, []winui.PointF{
			{X: float32(float64(p0.X) + px), Y: float32(float64(p0.Y) + py)},
			{X: float32(float64(p1.X) + px), Y: float32(float64(p1.Y) + py)},
			{X: float32(float64(p1.X) - px), Y: float32(float64(p1.Y) - py)},
			{X: float32(float64(p0.X) - px), Y: float32(float64(p0.Y) - py)},
		})
	}
	return polys
}

func buildCirclePolygon(cx, cy, r float64, segments int) []winui.PointF {
	if r < 0.5 {
		r = 0.5
	}
	poly := make([]winui.PointF, segments)
	for i := 0; i < segments; i++ {
		ang := 2 * math.Pi * float64(i) / float64(segments)
		poly[i] = winui.PointF{
			X: float32(cx + r*math.Cos(ang)),
			Y: float32(cy + r*math.Sin(ang)),
		}
	}
	return poly
}
