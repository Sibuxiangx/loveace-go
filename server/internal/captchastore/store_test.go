package captchastore

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func mustOpen(t *testing.T, dir string, opts ...Option) *Store {
	t.Helper()
	s, err := Open(dir, opts...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func sampleRecords() []Record {
	img1 := []byte{0x47, 0x49, 0x46, 0x38, 0x01, 0x02, 0x03}
	img2 := []byte{0x47, 0x49, 0x46, 0x38, 0x09, 0x08, 0x07, 0x06}
	// TS 由 Store 的时钟写入:测试注入固定时钟后全部等于 1700000000。
	return []Record{
		{TS: 1700000000, Verdict: Accepted, Code: "ab2d", Image: img1},
		{TS: 1700000000, Verdict: Rejected, Code: "zz9q", Image: img2},
		{TS: 1700000000, Verdict: Accepted, Code: "wxyz", Image: img1},
		{TS: 1700000000, Verdict: Rejected, Code: "", Image: nil},
		{TS: 1700000000, Verdict: Accepted, Code: "k3bn", Image: img2},
	}
}

// waitWritten 等待写入计数到位(Record 异步,轮转测试需要同步点)。
func waitWritten(t *testing.T, s *Store, n int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w, _ := s.Stats(); w >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待写入 %d 条超时", n)
}

func TestRoundtripCompressedOnClose(t *testing.T) {
	dir := t.TempDir()
	want := sampleRecords()
	// 固定时钟:TS 断言可确定。
	s := mustOpen(t, dir, WithNow(func() time.Time { return time.Unix(1700000000, 0) }))
	for _, r := range want {
		s.Record(r.Image, r.Code, r.Verdict == Accepted)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// 关停后:当日裸文件应已压缩删除,只剩一个 .zst。
	entries, _ := os.ReadDir(dir)
	zst, raw := 0, 0
	for _, e := range entries {
		switch {
		case filepath.Ext(e.Name()) == ".zst":
			zst++
		case filepath.Ext(e.Name()) == ".raw":
			raw++
		}
	}
	if zst != 1 || raw != 0 {
		t.Fatalf("关停后应恰 1 个 .zst、0 个 .raw,实际 zst=%d raw=%d", zst, raw)
	}
	got, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("记录数 %d != %d", len(got), len(want))
	}
	for i := range want {
		if got[i].TS != want[i].TS || got[i].Verdict != want[i].Verdict || got[i].Code != want[i].Code {
			t.Fatalf("record[%d] 不一致: %+v vs %+v", i, got[i], want[i])
		}
		if string(got[i].Image) != string(want[i].Image) {
			t.Fatalf("record[%d] image 不一致", i)
		}
	}
	written, dropped := s.Stats()
	if written != int64(len(want)) || dropped != 0 {
		t.Fatalf("stats written=%d dropped=%d", written, dropped)
	}
}

func TestDayRotation(t *testing.T) {
	dir := t.TempDir()
	// 可变时钟:闭包跨 goroutine 读,必须持锁(否则 -race 报数据竞争)。
	var mu sync.Mutex
	now := time.Unix(1700000000, 0)
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	s := mustOpen(t, dir, WithNow(clock))
	for i := 0; i < 2; i++ {
		s.Record([]byte{byte(i)}, "aa11", true)
	}
	waitWritten(t, s, 2)
	mu.Lock()
	now = now.Add(24 * time.Hour)
	mu.Unlock()
	for i := 0; i < 2; i++ {
		s.Record([]byte{byte(i + 10)}, "bb22", false)
	}
	waitWritten(t, s, 4)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("应有 4 条,实际 %d", len(got))
	}
	if got[0].Code != "aa11" || got[3].Code != "bb22" {
		t.Fatalf("跨日顺序错乱: %+v", got)
	}
	// 两个日期各自一个 .zst
	days := map[string]bool{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if day, _, _, isZst, ok := parseName(e.Name()); ok && isZst {
			days[day] = true
		}
	}
	if len(days) != 2 {
		t.Fatalf("应有 2 个日期的压缩文件,实际 %v", days)
	}
}

func TestSizeRotation(t *testing.T) {
	dir := t.TempDir()
	s := mustOpen(t, dir, WithMaxFileBytes(1))
	for i := 0; i < 3; i++ {
		s.Record([]byte{1, 2, 3}, "cccc", true)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("应有 3 条,实际 %d", len(got))
	}
	entries, _ := os.ReadDir(dir)
	zst := 0
	for _, e := range entries {
		if _, _, _, isZst, ok := parseName(e.Name()); ok && isZst {
			zst++
		}
	}
	if zst < 2 {
		t.Fatalf("小文件阈值下应产生 ≥2 个压缩分片,实际 %d", zst)
	}
}

func TestDropAfterClose(t *testing.T) {
	dir := t.TempDir()
	s := mustOpen(t, dir)
	s.Record([]byte{1}, "abcd", true)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	before, _ := s.Stats()
	s.Record([]byte{2}, "eeee", false)
	_, dropped := s.Stats()
	if dropped != 1 {
		t.Fatalf("Close 后应丢弃并计数,dropped=%d (before=%d)", dropped, before)
	}
}

func TestRetentionSweep(t *testing.T) {
	dir := t.TempDir()
	// 预置一个"过期"的 .zst(mtime 设为很久以前)。
	old := filepath.Join(dir, zstName("20200101", 0))
	if err := os.WriteFile(old, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-400 * 24 * time.Hour)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}
	s := mustOpen(t, dir) // 默认保留 180 天 → 20200101 应被清
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("过期 .zst 应被清理,err=%v", err)
	}
}
