package releasebundle

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// 同一几何源生成三种系统图标，避免维护无法追溯的二进制源文件。
func writeIcons(root string) error {
	im := image.NewNRGBA(image.Rect(0, 0, 512, 512))
	ink := color.NRGBA{35, 63, 50, 255}
	paper := color.NRGBA{247, 245, 238, 255}
	for y := 0; y < 512; y++ {
		for x := 0; x < 512; x++ {
			var r, g, b, a float64
			for sy := 0; sy < 4; sy++ {
				for sx := 0; sx < 4; sx++ {
					px, py := float64(x)+(float64(sx)+.5)/4, float64(y)+(float64(sy)+.5)/4
					dx, dy := math.Abs(px-256)-176, math.Abs(py-256)-176
					inside := math.Hypot(math.Max(dx, 0), math.Max(dy, 0))+math.Min(math.Max(dx, dy), 0) <= 64
					if !inside {
						continue
					}
					c := paper
					d := math.Hypot(px-256, py-256)
					if d >= 108 && d <= 138 {
						c = ink
					}
					// O 的开口保留一处页角。
					if px > 280 && py < 205 {
						c = paper
					}
					if px > 282 && px < 310 && py > 116 && py < 209 {
						c = ink
					}
					if px > 282 && px < 375 && py > 116 && py < 144 {
						c = ink
					}
					r += float64(c.R)
					g += float64(c.G)
					b += float64(c.B)
					a += 255
				}
			}
			if a > 0 {
				n := a / 255
				im.SetNRGBA(x, y, color.NRGBA{uint8(r/n + .5), uint8(g/n + .5), uint8(b/n + .5), uint8(a/16 + .5)})
			}
		}
	}
	var png512 bytes.Buffer
	if err := png.Encode(&png512, im); err != nil {
		return err
	}
	small := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			small.SetNRGBA(x, y, im.NRGBAAt(2*x, 2*y))
		}
	}
	var png256 bytes.Buffer
	if err := png.Encode(&png256, small); err != nil {
		return err
	}
	ico := make([]byte, 22)
	binary.LittleEndian.PutUint16(ico[2:], 1)
	binary.LittleEndian.PutUint16(ico[4:], 1)
	binary.LittleEndian.PutUint16(ico[10:], 1)
	binary.LittleEndian.PutUint16(ico[12:], 32)
	binary.LittleEndian.PutUint32(ico[14:], uint32(png256.Len()))
	binary.LittleEndian.PutUint32(ico[18:], 22)
	ico = append(ico, png256.Bytes()...)
	icns := make([]byte, 16)
	copy(icns, "icns")
	binary.BigEndian.PutUint32(icns[4:], uint32(16+png512.Len()))
	copy(icns[8:], "ic09")
	binary.BigEndian.PutUint32(icns[12:], uint32(8+png512.Len()))
	icns = append(icns, png512.Bytes()...)
	for name, data := range map[string][]byte{"ownward.png": png512.Bytes(), "ownward.ico": ico, "ownward.icns": icns} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0644); err != nil {
			return err
		}
	}
	return nil
}
