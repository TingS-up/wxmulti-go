// mkicon 生成应用图标与界面 logo：
// 底图是官方微信双气泡（assets/wechat-mark.png），右上角加「多开」。
//
//	go run ./tools/mkicon app.ico frontend\logo.png [preview.png]
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp"
)

var sizes = []int{16, 20, 24, 32, 40, 48, 64, 128, 256}

const (
	defaultMark = "assets/wechat-mark.png"
)

func main() {
	out := "app.ico"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	logoPNG := ""
	if len(os.Args) > 2 {
		logoPNG = os.Args[2]
	}
	previewPNG := ""
	if len(os.Args) > 3 {
		previewPNG = os.Args[3]
	}
	markPath := defaultMark
	if v := os.Getenv("MKICON_MARK"); v != "" {
		markPath = v
	}

	mark, err := loadMark(markPath)
	if err != nil {
		fmt.Println("! 读不到底图:", err)
		os.Exit(1)
	}
	cnFont := loadChineseFont()

	var images [][]byte
	var meta []entry
	for _, s := range sizes {
		img := compose(s, mark, cnFont, false, true)
		var data []byte
		if s >= 256 {
			var buf bytes.Buffer
			if err := png.Encode(&buf, img); err != nil {
				panic(err)
			}
			data = buf.Bytes()
		} else {
			data = encodeDIB(img)
		}
		meta = append(meta, entry{size: s, data: data})
		images = append(images, data)
	}

	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint16(0))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint16(len(meta)))
	offset := 6 + 16*len(meta)
	for _, m := range meta {
		w := byte(m.size)
		if m.size >= 256 {
			w = 0
		}
		buf.Write([]byte{w, w, 0, 0})
		binary.Write(&buf, binary.LittleEndian, uint16(1))
		binary.Write(&buf, binary.LittleEndian, uint16(32))
		binary.Write(&buf, binary.LittleEndian, uint32(len(m.data)))
		binary.Write(&buf, binary.LittleEndian, uint32(offset))
		offset += len(m.data)
	}
	for _, d := range images {
		buf.Write(d)
	}
	writeFile(out, buf.Bytes())
	fmt.Printf("wrote %s (%d bytes, sizes %v)\n", out, buf.Len(), sizes)

	if logoPNG != "" {
		// 界面左上角那个 34px 的 logo：铺白底圆角板，否则绿气泡贴在绿色头部上看不见
		var b bytes.Buffer
		if err := png.Encode(&b, compose(192, mark, cnFont, true, false)); err != nil {
			panic(err)
		}
		writeFile(logoPNG, b.Bytes())
		fmt.Printf("wrote %s (%d bytes)\n", logoPNG, b.Len())
	}
	if previewPNG != "" {
		var b bytes.Buffer
		if err := png.Encode(&b, compose(256, mark, cnFont, false, true)); err != nil {
			panic(err)
		}
		writeFile(previewPNG, b.Bytes())
		fmt.Printf("wrote %s (preview 256px)\n", previewPNG)
	}
	if sheetPath := os.Getenv("MKICON_SHEET"); sheetPath != "" {
		writeSheet(sheetPath, mark, cnFont)
	}
}

// writeSheet 把各尺寸排到一张深色底图上，方便肉眼检查「多开」有没有压到气泡、有没有被切掉。
// 上行是原尺寸，下行是 4 倍最近邻放大（小尺寸才看得清笔画）。
func writeSheet(path string, mark image.Image, cnFont *opentype.Font) {
	const zoom = 4
	const pad = 8
	zoomSizes := []int{16, 24, 32, 48, 64}

	row1H := 0
	w1 := pad
	for _, s := range sizes {
		if s > row1H {
			row1H = s
		}
		w1 += s + pad
	}
	row2H := 0
	w2 := pad
	for _, s := range zoomSizes {
		if s*zoom > row2H {
			row2H = s * zoom
		}
		w2 += s*zoom + pad
	}
	w := w1
	if w2 > w {
		w = w2
	}
	h := pad + row1H + pad + row2H + pad

	canvas := image.NewNRGBA(image.Rect(0, 0, w, h))
	bg := color.NRGBA{0x3A, 0x3F, 0x45, 0xFF}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			canvas.SetNRGBA(x, y, bg)
		}
	}

	x := pad
	for _, s := range sizes {
		icon := compose(s, mark, cnFont, false, true)
		draw.Draw(canvas, image.Rect(x, pad+row1H-s, x+s, pad+row1H), icon, image.Point{}, draw.Over)
		x += s + pad
	}
	x = pad
	y0 := pad + row1H + pad
	for _, s := range zoomSizes {
		icon := compose(s, mark, cnFont, false, true)
		dst := image.Rect(x, y0+row2H-s*zoom, x+s*zoom, y0+row2H)
		draw.NearestNeighbor.Scale(canvas, dst, icon, icon.Bounds(), draw.Over, nil)
		x += s*zoom + pad
	}

	var b bytes.Buffer
	if err := png.Encode(&b, canvas); err != nil {
		panic(err)
	}
	writeFile(path, b.Bytes())
	fmt.Printf("wrote %s (sheet %dx%d)\n", path, w, h)
}

type entry struct {
	size int
	data []byte
}

func writeFile(path string, data []byte) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		os.MkdirAll(dir, 0o755)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		panic(err)
	}
}

// loadMark 读底图并裁掉四周透明边，让气泡尽量撑满图标
func loadMark(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	minX, minY, maxX, maxY := b.Max.X, b.Max.Y, b.Min.X, b.Min.Y
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := src.At(x, y).RGBA()
			if a > 8<<8 {
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
				if x > maxX {
					maxX = x
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if minX >= maxX || minY >= maxY {
		return src, nil
	}
	return src.(interface {
		SubImage(r image.Rectangle) image.Image
	}).SubImage(image.Rect(minX, minY, maxX+1, maxY+1)), nil
}

// ---------------------------------------------------------------- 字体

func loadChineseFont() *opentype.Font {
	candidates := []string{
		`C:\Windows\Fonts\msyhbd.ttc`,
		`C:\Windows\Fonts\msyh.ttc`,
		`C:\Windows\Fonts\simhei.ttf`,
		`C:\Windows\Fonts\Dengb.ttf`,
		`C:\Windows\Fonts\simsun.ttc`,
	}
	for _, path := range candidates {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if strings.HasSuffix(strings.ToLower(path), ".ttc") {
			coll, err := opentype.ParseCollection(raw)
			if err != nil || coll.NumFonts() == 0 {
				continue
			}
			if f, err := coll.Font(0); err == nil {
				return f
			}
			continue
		}
		if f, err := opentype.Parse(raw); err == nil {
			return f
		}
	}
	fmt.Println("! 没找到可用的中文字体，大尺寸图标将不显示「多开」字样")
	return nil
}

// ---------------------------------------------------------------- 绘制

func clamp(v, lo, hi float64) float64 { return math.Min(math.Max(v, lo), hi) }

// sdRoundBox 圆角矩形有符号距离（像素，内部为负）
func sdRoundBox(px, py, cx, cy, hx, hy, r float64) float64 {
	qx := math.Abs(px-cx) - (hx - r)
	qy := math.Abs(py-cy) - (hy - r)
	ax := math.Max(qx, 0)
	ay := math.Max(qy, 0)
	return math.Hypot(ax, ay) + math.Min(math.Max(qx, qy), 0) - r
}

func coverage(d float64) float64 { return clamp(0.5-d, 0, 1) }

var (
	textGreen = color.RGBA{0x0F, 0xA0, 0x52, 0xFF}
	tileTop   = color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}
	tileBot   = color.RGBA{0xEC, 0xF7, 0xF0, 0xFF}
	tileEdge  = color.RGBA{0xDC, 0xEB, 0xE2, 0xFF}
)

// compose 把底图缩放到目标尺寸，可选白底板、可选「多开」文字
func compose(size int, mark image.Image, cnFont *opentype.Font, withTile, withText bool) *image.NRGBA {
	s := float64(size)
	img := image.NewNRGBA(image.Rect(0, 0, size, size))

	// 1. 白底板
	if withTile {
		radius := s * 0.225
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				px, py := float64(x)+0.5, float64(y)+0.5
				d := sdRoundBox(px, py, s/2, s/2, s/2, s/2, radius)
				cov := coverage(d)
				if cov <= 0 {
					continue
				}
				t := clamp(py/s, 0, 1)
				cur := color.NRGBA{
					R: uint8(float64(tileTop.R) + (float64(tileBot.R)-float64(tileTop.R))*t),
					G: uint8(float64(tileTop.G) + (float64(tileBot.G)-float64(tileTop.G))*t),
					B: uint8(float64(tileTop.B) + (float64(tileBot.B)-float64(tileTop.B))*t),
					A: uint8(cov * 255),
				}
				if d > -math.Max(1, s*0.008) {
					cur.R = tileEdge.R
					cur.G = tileEdge.G
					cur.B = tileEdge.B
				}
				img.SetNRGBA(x, y, cur)
			}
		}
	}

	// 2. 底图：按比例缩放，有文字时贴底，没文字时居中
	mb := mark.Bounds()
	aspect := float64(mb.Dx()) / float64(mb.Dy())
	var artW float64
	if withText {
		artW = s * 0.89
		if size <= 32 {
			artW = s * 0.80
		}
	} else if withTile {
		artW = s * 0.80
	} else {
		artW = s * 0.98
	}
	artH := artW / aspect
	if artH > s*0.95 {
		artH = s * 0.95
		artW = artH * aspect
	}
	artX := (s - artW) / 2
	var artY float64
	if withText {
		artY = s*0.995 - artH
	} else {
		artY = (s - artH) / 2
	}
	draw.CatmullRom.Scale(img,
		image.Rect(int(math.Round(artX)), int(math.Round(artY)),
			int(math.Round(artX+artW)), int(math.Round(artY+artH))),
		mark, mb, draw.Over, nil)

	// 3. 右上角「多开」：压着气泡图的顶边往上留一点空
	if withText && cnFont != nil {
		drawCaption(img, size, cnFont, artY)
	}
	return img
}

// drawCaption 右上角写「多开」：先描白边再填绿，深浅背景都看得清。
// artTop 是气泡图的顶边，文字底线贴着它往上留一丁点空 —— 这样字能尽量大，又不会压到气泡。
func drawCaption(img *image.NRGBA, size int, cnFont *opentype.Font, artTop float64) {
	s := float64(size)
	// 字号按尺寸分档：越小相对越大，否则挤不出笔画
	scale := 0.225
	switch {
	case size <= 32:
		scale = 0.330
	case size <= 63:
		scale = 0.275
	}
	face, err := opentype.NewFace(cnFont, &opentype.FaceOptions{
		Size:    s * scale,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return
	}
	defer face.Close()

	text := "多开"
	base := &font.Drawer{Dst: img, Face: face}
	w := base.MeasureString(text)
	right := fixed.I(int(math.Round(s * 0.975)))
	// 底线贴着气泡顶边往上让一点；极端情况下也不许顶出图标外
	baselineY := artTop - s*0.014
	if baselineY < s*0.02 {
		baselineY = s * 0.02
	}
	baseline := fixed.I(int(math.Round(baselineY)))
	start := fixed.Point26_6{X: right - w, Y: baseline}

	// 白描边只在有大字号时加；小尺寸下笔画本来就 1-2px，再描边会把字吃掉
	if size > 32 {
		ring := math.Max(1.0, s*0.013)
		rp := fixed.Int26_6(int(math.Round(ring * 64)))
		halo := &font.Drawer{Dst: img, Face: face, Src: image.NewUniform(color.NRGBA{255, 255, 255, 255})}
		for _, off := range [][2]fixed.Int26_6{
			{-rp, 0}, {rp, 0}, {0, -rp}, {0, rp},
			{-rp, -rp}, {rp, -rp}, {-rp, rp}, {rp, rp},
		} {
			halo.Dot = fixed.Point26_6{X: start.X + off[0], Y: start.Y + off[1]}
			halo.DrawString(text)
		}
	}
	fill := &font.Drawer{Dst: img, Face: face, Src: image.NewUniform(textGreen), Dot: start}
	fill.DrawString(text)
}

// encodeDIB 把图像编码成 ICO 里的 32 位 DIB
func encodeDIB(img *image.NRGBA) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint32(40))
	binary.Write(&buf, binary.LittleEndian, int32(w))
	binary.Write(&buf, binary.LittleEndian, int32(h*2))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint16(32))
	binary.Write(&buf, binary.LittleEndian, uint32(0))
	binary.Write(&buf, binary.LittleEndian, uint32(w*h*4))
	binary.Write(&buf, binary.LittleEndian, int32(0))
	binary.Write(&buf, binary.LittleEndian, int32(0))
	binary.Write(&buf, binary.LittleEndian, uint32(0))
	binary.Write(&buf, binary.LittleEndian, uint32(0))
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			c := img.NRGBAAt(x, y)
			buf.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	rowBytes := ((w + 31) / 32) * 4
	for y := 0; y < h; y++ {
		buf.Write(make([]byte, rowBytes))
	}
	return buf.Bytes()
}
