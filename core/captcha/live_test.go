package captcha

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/config"
	"github.com/Sibuxiangx/loveace-go/core/crypto"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

const (
	server    = "https://vpn2.aufe.edu.cn"
	bogusUser = "20000001"
	bogusPass = "ProbeZk2024a"
)

func defaultModelPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join(".", "model", "common.onnx")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("模型不存在(%v),跳过 live 测试", err)
	}
	return p
}

// TestLiveSolveFixtures 对 testdata 真图跑一次完整解码,报告 4 位有效率。
// 需要 core/captcha/model/common.onnx + onnxruntime 动态库。
func TestLiveSolveFixtures(t *testing.T) {
	model := defaultModelPath(t)
	s, err := New(model, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatalf("testdata: %v", err)
	}
	valid, total := 0, 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".gif") {
			continue
		}
		total++
		raw, err := os.ReadFile(filepath.Join("testdata", e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		code, ok, err := s.Solve(raw)
		if err != nil {
			t.Fatalf("Solve %s: %v", e.Name(), err)
		}
		if ok {
			valid++
		}
		t.Logf("%-12s -> %-6q ok=%v", e.Name(), code, ok)
	}
	t.Logf("有效解码 %d/%d", valid, total)
	if total == 0 {
		t.Skip("testdata 无 GIF")
	}
}

// TestLiveOracleAcceptance 把 Go 解码的真实猜测提交网关判卷,报告接受率。
// 这是 M1 的验收门:接受率应落在 ddddocr beta 的实测基线(33.7%)附近或更高
// (受限解码应≥)。会烧假账号失败额度(本机 IP 进验证码模式,~3 分钟自愈),
// 因此默认跳过,需要显式 LOVEACE_CAPTCHA_ORACLE=1。
func TestLiveOracleAcceptance(t *testing.T) {
	if os.Getenv("LOVEACE_CAPTCHA_ORACLE") != "1" {
		t.Skip("需要 LOVEACE_CAPTCHA_ORACLE=1")
	}
	model := defaultModelPath(t)
	s, err := New(model, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	tr := transport.NewHTTPTransport(8)
	// 预热:假账号连续失败登录,直到 auth 页 RndImg=1(判卷前提)。
	armed := false
	for i := 0; i < 4; i++ {
		w := session.New(bogusUser, bogusPass, bogusPass, tr)
		st := w.ECLogin(context.Background())
		t.Logf("warm #%d -> %s", i+1, st.Outcome)
		if st.Outcome == session.ECCaptchaRequired {
			armed = true
			break
		}
		if st.Outcome != session.ECInvalidCredentials {
			t.Fatalf("warm 异常 outcome=%s", st.Outcome)
		}
		time.Sleep(1200 * time.Millisecond)
	}
	if !armed {
		t.Fatalf("未能进入验证码模式")
	}

	const n = 30
	okCount, badCount, skipped, weird := 0, 0, 0, 0
	for i := 0; i < n; i++ {
		samp := session.New(bogusUser, bogusPass, bogusPass, transport.NewHTTPTransport(8))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		ec, err := samp.ECFetchAuthContext(ctx)
		if err != nil {
			t.Fatalf("auth: %v", err)
		}
		img, err := samp.ECFetchCaptchaImage(ctx, ec)
		cancel()
		if err != nil {
			t.Fatalf("img: %v", err)
		}
		code, ok, err := s.Solve(img)
		if err != nil {
			t.Fatalf("solve: %v", err)
		}
		if !ok {
			skipped++
			t.Logf("[%02d] 解码无效 → 弃图不提交", i)
			time.Sleep(800 * time.Millisecond)
			continue
		}
		res, err := submitOnce(ec, code)
		if err != nil {
			t.Fatalf("submit: %v", err)
		}
		switch {
		case strings.HasPrefix(res, "20004"):
			okCount++
		case strings.HasPrefix(res, "20023"):
			badCount++
		default:
			weird++
			t.Logf("[%02d] 异常判定: %s", i, res)
		}
		t.Logf("[%02d] code=%-6s => %s", i, code, res)
		time.Sleep(1500 * time.Millisecond)
	}
	judged := okCount + badCount
	t.Logf("oracle 结果: 接受 %d / 判错 %d / 弃图 %d / 异常 %d → 接受率 %.1f%%",
		okCount, badCount, skipped, weird, 100*float64(okCount)/float64(max(1, judged)))
	if weird > 0 {
		t.Errorf("出现异常判定(判卷机制异常),检查响应")
	}
}

// submitOnce 用假凭据 + 指定码提交一次判卷(复刻 ecSubmitLogin 的表单)。
func submitOnce(ec *session.ECAuthContext, code string) (string, error) {
	s := session.New(bogusUser, bogusPass, bogusPass, transport.NewHTTPTransport(8))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	enc, err := crypto.RSAEncryptPKCS1v15(bogusPass+"_"+ec.CSRF, ec.RSAKey, ec.RSAExp)
	if err != nil {
		return "", err
	}
	form := [][2]string{
		{"svpn_rand_code", code},
		{"mitm", ""},
		{"svpn_req_randcode", ec.CSRF},
		{"svpn_name", bogusUser},
		{"svpn_password", enc},
	}
	resp, err := s.RawPost(ctx, config.JoinURL(server, config.ECLoginPsw), form,
		[][2]string{{"Cookie", "TWFID=" + ec.TWFID}}, 0)
	if err != nil {
		return "", err
	}
	body := resp.BodyText()
	switch {
	case strings.Contains(body, "<Result>1</Result>"):
		return "UNEXPECTED_SUCCESS", nil
	case strings.Contains(body, "20023"), strings.Contains(body, "characters are incorrect"):
		return "20023 captcha_wrong", nil
	case strings.Contains(body, "20004"), strings.Contains(body, "Invalid username or password"):
		return "20004 captcha_ok_creds_wrong", nil
	default:
		return fmt.Sprintf("other: %.160s", body), nil
	}
}
