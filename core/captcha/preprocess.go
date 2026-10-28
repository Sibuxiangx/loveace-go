package captcha

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
)

// 目标高度:ddddocr 官方模型把图放大到高 64(宽按比例)。
const targetH = 64

// Pillow 把 P 模式(调色板)图像的 resize 强制为 NEAREST(Image.resize 的
// "1"/"P" 特例,LANCZOS 参数被忽略),即输出像素 i 取输入像素
// floor((i+0.5)*in/out)。对 63→201 与 20→64 可证无 tie,行为完全确定。
// 灰度用 Pillow 的 ITU-R 601 定点矩阵(含 0x8000 舍入)。
func nearestIndex(i, inSize, outSize int) int {
	return int((float64(i) + 0.5) * float64(inSize) / float64(outSize))
}

// preprocessGIF 复刻 ddddocr 预处理,返回 [1][1][64][W] 行主序 float32 张量数据
// 与目标宽 W。
func preprocessGIF(data []byte) ([]float32, int, error) {
	img, err := gif.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, fmt.Errorf("gif decode: %w", err)
	}
	pal, ok := img.(*image.Paletted)
	if !ok {
		return nil, 0, errors.New("unsupported: 非 P 模式调色板 GIF")
	}
	if len(pal.Palette) == 0 {
		return nil, 0, errors.New("empty palette")
	}
	b := pal.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, 0, errors.New("empty image")
	}
	// Python: int(w * (64 / h)) —— float64 运算后向零截断,与 CPython 一致。
	newW := int(float64(w) * (64.0 / float64(h)))
	if newW <= 0 {
		return nil, 0, fmt.Errorf("degenerate size w=%d h=%d", w, h)
	}

	grayAt := func(x, y int) float32 {
		idx := pal.Pix[(y-b.Min.Y)*pal.Stride+(x-b.Min.X)]
		c := pal.Palette[idx]
		var r, g, bl uint8
		if rgba, isRGBA := c.(color.RGBA); isRGBA {
			r, g, bl = rgba.R, rgba.G, rgba.B
		} else {
			rr, gg, bb, _ := c.RGBA() // 16 位预乘;调色板不透明,>>8 即 8 位
			r, g, bl = uint8(rr>>8), uint8(gg>>8), uint8(bb>>8)
		}
		l := (19595*int(r) + 38470*int(g) + 7471*int(bl) + 0x8000) >> 16
		return (float32(l)/255.0 - 0.5) / 0.5
	}

	out := make([]float32, targetH*newW)
	for y := 0; y < targetH; y++ {
		sy := nearestIndex(y, h, targetH)
		for x := 0; x < newW; x++ {
			sx := nearestIndex(x, w, newW)
			out[y*newW+x] = grayAt(b.Min.X+sx, b.Min.Y+sy)
		}
	}
	return out, newW, nil
}
