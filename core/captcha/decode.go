package captcha

// decode 把模型输出 logits(布局 [T,K] 行主序,K=最后一维类别数)按 ddddocr
// 语义解码成提交码。
//
// ddddocr 的 CTC 塌缩(逐字面复刻,与教科书式"先去重再删 blank"不同——blank
// 会重置去重游标,故 [5,0,5] 会输出两个 5):
//
//	last_item = 0
//	for item in argmax(...):
//	    if item == last_item: continue
//	    last_item = item
//	    if item != 0: result.append(charset[item])
//
// 差异:argmax 只在 allowed(= [blank 0] + 合法字符的全部下标)里取,
// 这是受限解码——网关字符集实测为 31 字符防混淆集,出集类别直接排除。
// 返回 ok=false 表示解码无效(非 4 位),调用方应弃图重取挑战。
func decode(logits []float32, t, k int) (string, bool) {
	if t <= 0 || k <= 0 || len(logits) < t*k {
		return "", false
	}
	out := make([]byte, 0, 4)
	last := 0
	for i := 0; i < t; i++ {
		row := logits[i*k : (i+1)*k]
		best := allowed[0]
		bestV := row[best]
		for _, idx := range allowed[1:] {
			if v := row[idx]; v > bestV {
				best, bestV = idx, v
			}
		}
		if best == last {
			continue
		}
		last = best
		if best != 0 {
			out = append(out, betaCharset[best]...)
		}
	}
	if len(out) != 4 {
		return "", false
	}
	return string(out), true
}

// allowed 是受限 argmax 的候选下标:blank(0) + 全部合法字符下标,升序。
var allowed = func() []int {
	out := make([]int, 0, len(allowedIndices)+1)
	out = append(out, 0) // blank 必须留在候选里,否则塌缩语义变形
	out = append(out, allowedIndices...)
	return out
}()
