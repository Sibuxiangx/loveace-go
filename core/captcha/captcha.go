// Package captcha 提供 EC 网关验证码的本地推理(ddddocr beta 模型,Go 原生 onnxruntime)。
//
// 管线完整复刻 ddddocr 官方模型路径:
//
//	GIF(P 模式)→ NEAREST 放大(高 64,宽 int(w*64/h);Pillow 对 P 模式强制 NEAREST,
//	LANCZOS 参数被忽略)→ 调色板→灰度(ITU-R 601 定点)→ (x/255-0.5)/0.5
//	→ [1,1,64,W] float32 → 受限 argmax → CTC 塌缩(仿 ddddocr 语义)
//
// 受限解码依据 2026-10-20 oracle 实测(loveace-go/AGENTS.md §5):该校验证码
// 字符集为 31 字符防混淆集(无 0/1/i/l/o);argmax 只在 [blank]+31 集内取值。
// 解码长度 ≠4 的结果判为无效(ok=false),调用方应弃图重取挑战,不浪费提交。
package captcha

// AllowedChars 是 oracle 实测的验证码字符集:数字+小写字母去掉 0/1/i/l/o,
// 共 31 个防混淆字符。程序化推导,避免手抄笔误。
var AllowedChars = buildAllowed()

func buildAllowed() map[rune]bool {
	m := make(map[rune]bool, 32)
	for c := byte('0'); c <= '9'; c++ {
		m[rune(c)] = true
	}
	for c := byte('a'); c <= 'z'; c++ {
		m[rune(c)] = true
	}
	delete(m, '0')
	delete(m, '1')
	delete(m, 'i')
	delete(m, 'l')
	delete(m, 'o')
	return m
}

// allowedIndices 是 betaCharset 中字符落在 AllowedChars 内的全部下标。
// 同一字符可能对应多个下标(实测 charset 有重复字符),全部保留参与 argmax;
// 无论选中哪个下标,解码出的字符都落在合法集内。
var allowedIndices = func() []int {
	var out []int
	for i, s := range betaCharset {
		r := []rune(s)
		if len(r) == 1 && AllowedChars[r[0]] {
			out = append(out, i)
		}
	}
	return out
}()

// Solver 把验证码图片解成提交码。ok=false 表示解码无效(非 4 位或出集),
// 调用方不应提交,直接换新挑战。
type Solver interface {
	Solve(imageData []byte) (code string, ok bool, err error)
}
