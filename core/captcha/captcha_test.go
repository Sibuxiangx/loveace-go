package captcha

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"math"
	"testing"
)

func TestAllowedCharset(t *testing.T) {
	if len(AllowedChars) != 31 {
		t.Fatalf("AllowedChars 应为 31 个,实际 %d", len(AllowedChars))
	}
	for _, bad := range []rune{'0', '1', 'i', 'l', 'o'} {
		if AllowedChars[bad] {
			t.Fatalf("字符 %q 不应在集合内", bad)
		}
	}
	if len(allowedIndices) < 31 {
		t.Fatalf("charset 内合法字符下标不足: %d", len(allowedIndices))
	}
	for _, idx := range allowedIndices {
		r := []rune(betaCharset[idx])
		if len(r) != 1 || !AllowedChars[r[0]] {
			t.Fatalf("allowedIndices 含非法项 %d→%q", idx, betaCharset[idx])
		}
	}
	if betaCharset[0] != "" {
		t.Fatalf("charset[0] 应为 blank 空串,实际 %q", betaCharset[0])
	}
}

// allowedIndexOf 返回某字符在 allowed 候选里的位置(测试辅助)。
func allowedIndexOf(t *testing.T, c byte) int {
	t.Helper()
	for _, idx := range allowed {
		if idx != 0 && betaCharset[idx] == string(rune(c)) {
			return idx
		}
	}
	t.Fatalf("字符 %q 不在 allowed 候选中", c)
	return -1
}

func TestDecodeSynthetic(t *testing.T) {
	// 4 个时间步,依次 argmax 到 a/b/c/d 的下标 → "abcd"
	picks := []byte{'a', 'b', 'c', 'd'}
	// 构造一个覆盖 5 个时间步、夹带 blank 的序列: a blank b blank c d
	seq := []int{
		allowedIndexOf(t, 'a'), 0,
		allowedIndexOf(t, 'b'), 0,
		allowedIndexOf(t, 'c'), allowedIndexOf(t, 'd'),
	}
	// k 取最大候选下标+1(模拟真实模型的类别数 = charset 大小)
	k := 0
	for _, idx := range allowed {
		if idx+1 > k {
			k = idx + 1
		}
	}
	tSteps := len(seq)
	logits := make([]float32, tSteps*k)
	// 把每个候选下标在各自时间步置高,其余(候选内)置低
	for step, want := range seq {
		for _, idx := range allowed {
			logits[step*k+idx] = -1
		}
		logits[step*k+want] = 3
	}
	// 注意:非候选下标(出集字符)永远打不过候选——受限解码的语义
	code, ok := decode(logits, tSteps, k)
	if !ok {
		t.Fatalf("应解码成功")
	}
	wantStr := string(picks)
	if code != wantStr {
		t.Fatalf("decode = %q, 期望 %q", code, wantStr)
	}

	// 非法长度: a blank a → a,a(2 位) → ok=false
	seq2 := []int{allowedIndexOf(t, 'a'), 0, allowedIndexOf(t, 'a')}
	logits2 := make([]float32, len(seq2)*k)
	for step, want := range seq2 {
		for _, idx := range allowed {
			logits2[step*k+idx] = -1
		}
		logits2[step*k+want] = 3
	}
	if _, ok := decode(logits2, len(seq2), k); ok {
		t.Fatalf("2 位解码应判无效")
	}
}

func TestPreprocessSyntheticGIF(t *testing.T) {
	// 2×1 调色板图: 两个像素 黑/白。放大到 64 高、宽 int(2*64/1)=128。
	pal := color.Palette{color.RGBA{0, 0, 0, 255}, color.RGBA{255, 255, 255, 255}}
	p := image.NewPaletted(image.Rect(0, 0, 2, 1), pal)
	p.Pix[0] = 0 // 黑
	p.Pix[1] = 1 // 白
	var buf bytes.Buffer
	if err := gif.Encode(&buf, p, nil); err != nil {
		t.Fatalf("gif encode: %v", err)
	}
	data, w, err := preprocessGIF(buf.Bytes())
	if err != nil {
		t.Fatalf("preprocess: %v", err)
	}
	if w != 128 {
		t.Fatalf("目标宽应 128,实际 %d", w)
	}
	if len(data) != 64*w {
		t.Fatalf("张量大小应 %d,实际 %d", 64*w, len(data))
	}
	// 左半来自黑像素 → -1;右半来自白像素 → +1(NEAREST: x<64 → 输入 0)
	for x := 0; x < w; x++ {
		want := float32(-1)
		if x >= 64 {
			want = 1
		}
		for y := 0; y < 64; y++ {
			if got := data[y*w+x]; math.Abs(float64(got-want)) > 1e-6 {
				t.Fatalf("data[%d,%d]=%v 期望 %v", x, y, got, want)
			}
		}
	}
}
