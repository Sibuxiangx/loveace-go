// Package captchastore 沉淀验证码判卷结果供后训练(DESIGN:验证码原生化 M3):
//
//   - accepted(金标签):码被网关接受 —— 自动解成功或人工手解通过;
//   - rejected(困难负样本):码被拒 —— 图 + 错误猜测,同样可进训练。
//
// 存储为日切文件:当前日写裸帧(captcha-YYYYMMDD.raw),日切/大小阈值/关停时
// 用 zstd 压成 captcha-YYYYMMDD[-seq].zst 并删除裸文件;启动时顺带压缩遗留
// 裸文件并按保留期清理过期 .zst。
//
// 帧格式(小端):u32 总长 | u8 verdict | i64 ts | u16 codeLen | code | u32 imgLen | img。
package captchastore

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Verdict 标记一次提交的判卷结论。
type Verdict uint8

const (
	Accepted Verdict = 1 // 码被网关接受(金标签)
	Rejected Verdict = 2 // 码被拒(困难负样本)
)

const (
	dayLayout        = "20060102"
	filePrefix       = "captcha-"
	defaultMaxFile   = int64(64 << 20) // 单裸文件上限(超出提前轮转)
	defaultKeep      = 180 * 24 * time.Hour
	defaultQueueSize = 256
)

// Record 是一条沉淀记录。
type Record struct {
	TS      int64 // unix 秒
	Verdict Verdict
	Code    string // 提交的码(自动解的猜测或人工手输)
	Image   []byte // 原始验证码 GIF
}

// Store 是追加式记录器:单写 goroutine,Record 非阻塞(队列满则丢弃并计数,
// 绝不阻塞登录路径)。
type Store struct {
	dir     string
	now     func() time.Time
	maxFile int64
	keep    time.Duration
	queue   int

	mu      sync.Mutex
	f       *os.File
	bw      *bufio.Writer
	day     string
	seq     int
	wbytes  int64
	written int64

	dropped atomic.Int64

	ch        chan Record
	done      chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// Option 定制存储行为(测试用)。
type Option func(*Store)

// WithNow 注入时钟(测试日切)。
func WithNow(fn func() time.Time) Option { return func(s *Store) { s.now = fn } }

// WithMaxFileBytes 覆盖单裸文件字节上限。
func WithMaxFileBytes(n int64) Option { return func(s *Store) { s.maxFile = n } }

// WithKeep 覆盖 .zst 保留期(负数 = 全部过期,测试清理用)。
func WithKeep(d time.Duration) Option { return func(s *Store) { s.keep = d } }

// WithQueue 覆盖写队列长度。
func WithQueue(n int) Option { return func(s *Store) { s.queue = n } }

// Open 建目录、清理遗留(压缩昨日裸文件、删除过期 .zst)、打开当日文件并
// 启动单写 goroutine。
func Open(dir string, opts ...Option) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		dir:     dir,
		now:     time.Now,
		maxFile: defaultMaxFile,
		keep:    defaultKeep,
		queue:   defaultQueueSize,
	}
	for _, o := range opts {
		o(s)
	}
	if err := s.sweep(); err != nil {
		return nil, err
	}
	today := s.dayOf(s.now())
	if err := s.planDay(today); err != nil {
		return nil, err
	}
	s.ch = make(chan Record, s.queue)
	s.done = make(chan struct{})
	s.wg.Add(1)
	go s.loop()
	return s, nil
}

func (s *Store) dayOf(t time.Time) string { return t.Format(dayLayout) }

// rawBase 是某日的裸文件基础名(captcha-YYYYMMDD)。
func rawBase(day string) string { return filePrefix + day }

// zstName 是某日第 seq 个压缩文件名。
func zstName(day string, seq int) string {
	if seq == 0 {
		return rawBase(day) + ".zst"
	}
	return fmt.Sprintf("%s-%d.zst", rawBase(day), seq)
}

func parseName(name string) (day string, seq int, isRaw, isZst bool, ok bool) {
	if !strings.HasPrefix(name, filePrefix) {
		return "", 0, false, false, false
	}
	rest := strings.TrimPrefix(name, filePrefix)
	switch {
	case strings.HasSuffix(rest, ".raw"):
		day = strings.TrimSuffix(rest, ".raw")
		isRaw = true
	case strings.HasSuffix(rest, ".zst"):
		rest = strings.TrimSuffix(rest, ".zst")
		if i := strings.LastIndex(rest, "-"); i >= 0 {
			if n, err := strconv.Atoi(rest[i+1:]); err == nil && n >= 0 {
				day, seq = rest[:i], n
			}
		} else {
			day = rest
		}
		isZst = true
	default:
		return "", 0, false, false, false
	}
	if _, err := time.Parse(dayLayout, day); err != nil {
		return "", 0, false, false, false
	}
	return day, seq, isRaw, isZst, true
}

// sweep 启动清理:压缩非当日遗留裸文件;删除超过保留期的 .zst。
// 返回当日已存在的裸文件基础名(若有,供续写)与当日已存在的最大 zst 序号。
func (s *Store) sweep() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	today := s.dayOf(s.now())
	todayRaw, todaySeq := "", 0
	for _, e := range entries {
		name := e.Name()
		day, seq, isRaw, isZst, ok := parseName(name)
		if !ok {
			continue
		}
		if isRaw && day == today {
			if seq > todaySeq || todayRaw == "" {
				todayRaw, todaySeq = rawBase(day), seq
			}
			continue
		}
		if isRaw {
			// 遗留裸文件(崩溃/重启残留):压缩后删除。
			if err := compressRaw(s.dir, rawBase(day)); err != nil {
				return fmt.Errorf("compress leftover %s: %w", name, err)
			}
			continue
		}
		if isZst {
			info, err := e.Info()
			if err == nil && s.now().Sub(info.ModTime()) > s.keep {
				if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
					return err
				}
			}
		}
	}
	if todayRaw != "" {
		// 当日已有裸文件:续写(序号让位于已有 zst)。
		s.day = today
		s.seq = todaySeq
		f, err := os.OpenFile(filepath.Join(s.dir, todayRaw+".raw"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		s.f = f
		s.bw = bufio.NewWriter(f)
	}
	return nil
}

// planDay 记录当前日与该日已有的最大 zst 序号。懒打开:此时不建文件,
// 首个写入才落盘 —— 空日不再产生孤儿 .raw。
func (s *Store) planDay(day string) error {
	s.day = day
	s.seq = 0
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	// 当日已有 zst(同日重启/轮转过):序号顺延,避免覆盖。
	for _, e := range entries {
		if d, seq, _, isZst, ok := parseName(e.Name()); ok && isZst && d == day && seq >= s.seq {
			s.seq = seq + 1
		}
	}
	return nil
}

// openFileLocked 打开(或续写)当前日的裸文件。s.mu 必须已持有。
func (s *Store) openFileLocked() error {
	if s.bw != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(s.dir, rawBase(s.day)+".raw"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	s.f = f
	s.bw = bufio.NewWriter(f)
	return nil
}

// rotateLocked 压缩并删除当前裸文件,为下一段记录让路。s.mu 必须已持有。
func (s *Store) rotateLocked() error {
	if s.bw == nil {
		return nil
	}
	if err := s.bw.Flush(); err != nil {
		return err
	}
	if err := s.f.Close(); err != nil {
		return err
	}
	s.f, s.bw = nil, nil
	if s.wbytes > 0 {
		if err := compressRaw(s.dir, rawBase(s.day)); err != nil {
			return err
		}
		s.seq++
	}
	s.wbytes = 0
	return nil
}

func compressRaw(dir, base string) error {
	in, err := os.Open(filepath.Join(dir, base+".raw"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer in.Close()
	// 压缩名撞车(同日同序号已有 zst):顺延序号。
	seq := 0
	name := zstName(dayOfBase(base), seq)
	for {
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			break
		}
		seq++
		name = fmt.Sprintf("%s-%d.zst", base, seq)
	}
	out, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	enc, err := zstd.NewWriter(out)
	if err != nil {
		out.Close()
		return err
	}
	if _, err := io.Copy(enc, in); err != nil {
		enc.Close()
		out.Close()
		return err
	}
	if err := enc.Close(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(filepath.Join(dir, base+".raw"))
}

// dayOfBase 从 captcha-YYYYMMDD 形态的基础名取日期串。
func dayOfBase(base string) string { return strings.TrimPrefix(base, filePrefix) }

func (s *Store) loop() {
	defer s.wg.Done()
	for {
		select {
		case r := <-s.ch:
			s.write(r)
		case <-s.done:
			for {
				select {
				case r := <-s.ch:
					s.write(r)
				default:
					return
				}
			}
		}
	}
}

// write 落一条记录(必要时先轮转)。s.mu 保护文件句柄与游标。
func (s *Store) write(r Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	day := s.dayOf(s.now())
	size := frameSize(r)
	if s.bw != nil && (day != s.day || s.wbytes+int64(size) > s.maxFile) {
		if err := s.rotateLocked(); err != nil {
			s.dropped.Add(1)
			return
		}
	}
	if s.bw == nil {
		if s.day != day {
			if err := s.planDay(day); err != nil {
				s.dropped.Add(1)
				return
			}
		}
		if err := s.openFileLocked(); err != nil {
			s.dropped.Add(1)
			return
		}
	}
	if _, err := s.bw.Write(appendFrame(nil, r)); err != nil {
		s.dropped.Add(1)
		return
	}
	if err := s.bw.Flush(); err != nil {
		s.dropped.Add(1)
		return
	}
	s.wbytes += int64(size)
	s.written++
}

const frameHeader = 4 + 1 + 8 + 2 + 4 // 含 4 字节总长前缀本身

func frameSize(r Record) int {
	return frameHeader + len(r.Code) + len(r.Image)
}

func appendFrame(dst []byte, r Record) []byte {
	total := uint32(frameSize(r))
	dst = binary.LittleEndian.AppendUint32(dst, total)
	dst = append(dst, byte(r.Verdict))
	dst = binary.LittleEndian.AppendUint64(dst, uint64(r.TS))
	dst = binary.LittleEndian.AppendUint16(dst, uint16(len(r.Code)))
	dst = append(dst, r.Code...)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(r.Image)))
	dst = append(dst, r.Image...)
	return dst
}

// Record 异步沉淀一条记录;写队列满或已关停时丢弃并计数(绝不阻塞调用方)。
func (s *Store) Record(img []byte, code string, accepted bool) {
	v := Rejected
	if accepted {
		v = Accepted
	}
	select {
	case <-s.done:
		s.dropped.Add(1)
		return
	default:
	}
	select {
	case s.ch <- Record{TS: s.now().Unix(), Verdict: v, Code: code, Image: img}:
	default:
		s.dropped.Add(1)
	}
}

// Close 排空队列、压缩当前裸文件。Close 之后的 Record 一律计入 dropped。
func (s *Store) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rotateLocked()
}

// Stats 返回累计写入/丢弃计数。
func (s *Store) Stats() (written, dropped int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.written, s.dropped.Load()
}

// LoadAll 按日期+序号顺序读出目录里的全部记录(训练侧/测试用;文件大时
// 应改用流式迭代,训练跑批前量级估计 ~1-2MB/天 zstd,全量载入可接受)。
func LoadAll(dir string) ([]Record, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type fileInfo struct {
		day  string
		seq  int
		name string
		zst  bool
	}
	var files []fileInfo
	for _, e := range entries {
		day, seq, isRaw, _, ok := parseName(e.Name())
		if !ok || isRaw {
			continue // 裸文件只可能是当日活动文件,历史记录一律读 .zst
		}
		files = append(files, fileInfo{day: day, seq: seq, name: e.Name()})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].day != files[j].day {
			return files[i].day < files[j].day
		}
		return files[i].seq < files[j].seq
	})
	var out []Record
	for _, fi := range files {
		recs, err := readFile(filepath.Join(dir, fi.name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", fi.name, err)
		}
		out = append(out, recs...)
	}
	return out, nil
}

func readFile(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".zst") {
		zr, err := zstd.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		r = zr.IOReadCloser()
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var out []Record
	for len(data) > 0 {
		rec, n, err := parseFrame(data)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
		data = data[n:]
	}
	return out, nil
}

func parseFrame(b []byte) (Record, int, error) {
	if len(b) < 4 {
		return Record{}, 0, io.ErrUnexpectedEOF
	}
	total := int(binary.LittleEndian.Uint32(b))
	if total < frameHeader || len(b) < total {
		return Record{}, 0, io.ErrUnexpectedEOF
	}
	body := b[4:total]
	if len(body) < frameHeader-4 { // body 不含 4 字节长度前缀
		return Record{}, 0, io.ErrUnexpectedEOF
	}
	r := Record{Verdict: Verdict(body[0])}
	body = body[1:]
	r.TS = int64(binary.LittleEndian.Uint64(body))
	body = body[8:]
	cl := int(binary.LittleEndian.Uint16(body))
	body = body[2:]
	if len(body) < cl {
		return Record{}, 0, io.ErrUnexpectedEOF
	}
	r.Code = string(body[:cl])
	body = body[cl:]
	if len(body) < 4 {
		return Record{}, 0, io.ErrUnexpectedEOF
	}
	il := int(binary.LittleEndian.Uint32(body))
	body = body[4:]
	if len(body) < il {
		return Record{}, 0, io.ErrUnexpectedEOF
	}
	r.Image = append([]byte(nil), body[:il]...)
	return r, total, nil
}
