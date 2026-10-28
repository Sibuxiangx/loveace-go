package config

import "testing"

func TestJoinURLNormalisesSlashes(t *testing.T) {
	got := JoinURL(JWCBase, "/main/academicInfo")
	want := "http://jwcxk2-aufe-edu-cn.vpn2.aufe.edu.cn:8118/main/academicInfo"
	if got != want {
		t.Fatalf("JoinURL = %q, want %q", got, want)
	}
	if JoinURL("http://x/", "http://y/z") != "http://y/z" {
		t.Fatalf("absolute path must pass through")
	}
}

func TestQueryAppendsAndEncodes(t *testing.T) {
	if got := WithQuery("http://x/a", [][2]string{{"k", "v w"}}); got != "http://x/a?k=v%20w" {
		t.Fatalf("WithQuery = %q", got)
	}
	if got := WithQuery("http://x/a?z=1", [][2]string{{"k", "v"}}); got != "http://x/a?z=1&k=v" {
		t.Fatalf("WithQuery = %q", got)
	}
}

func TestFormEncodeMatchesReference(t *testing.T) {
	body := FormEncode([][2]string{
		{"zxjxjhh", "2023-2024-1-1"},
		{"kch", ""},
	})
	want := "zxjxjhh=2023-2024-1-1&kch="
	if body != want {
		t.Fatalf("FormEncode = %q, want %q", body, want)
	}
}

func TestURLEncode(t *testing.T) {
	cases := map[string]string{
		"abc123":       "abc123",
		"a b":          "a%20b",
		"2026年1月3日":    "2026%E5%B9%B41%E6%9C%883%E6%97%A5",
		"a+b":          "a%2Bb",
		"a&b=c":        "a%26b%3Dc",
		"~._-":         "~._-",
		"":             "",
		"宽ＡＢＣfullwide": "%E5%AE%BD%EF%BC%A1%EF%BC%A2%EF%BC%A3fullwide",
	}
	for in, want := range cases {
		if got := URLEncode(in); got != want {
			t.Errorf("URLEncode(%q) = %q, want %q", in, got, want)
		}
	}
}
