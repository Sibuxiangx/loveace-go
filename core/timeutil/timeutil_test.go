package timeutil

import (
	"testing"
	"time"
)

func TestChineseDatesConvert(t *testing.T) {
	if got := ChineseDateToStandard("2026年1月3日"); got != "2026-01-03" {
		t.Fatalf("got %q", got)
	}
	if got := ChineseDateToStandard("2026-01-03"); got != "2026-01-03" {
		t.Fatalf("got %q", got)
	}
	if got := ChineseDateToStandard("无"); got != "无" {
		t.Fatalf("got %q", got)
	}
	if got := ChineseDateToStandard("2026年12月31号"); got != "2026-12-31" {
		t.Fatalf("got %q", got)
	}
}

func TestISOExpiry(t *testing.T) {
	if !IsUnexpired(nil) {
		t.Fatal("nil expires means unexpired")
	}
	past := "2000-01-01T00:00:00Z"
	future := "2999-01-01T00:00:00Z"
	if IsUnexpired(&past) {
		t.Fatal("past must be expired")
	}
	if !IsUnexpired(&future) {
		t.Fatal("future must be unexpired")
	}
	bad := "not-a-date"
	if IsUnexpired(&bad) {
		t.Fatal("unparseable must be expired")
	}
}

func TestYMDIsWellFormed(t *testing.T) {
	s := TodayYMD()
	if len(s) != 10 {
		t.Fatalf("TodayYMD = %q", s)
	}
	if _, ok := ParseYMD(s); !ok {
		t.Fatalf("ParseYMD(%q) failed", s)
	}
}

func TestYMDHoursAgo(t *testing.T) {
	if got := YMDHoursAgo(7); len(got) != 10 {
		t.Fatalf("YMDHoursAgo(7) = %q", got)
	}
}

func TestRFC3339Millis(t *testing.T) {
	got := RFC3339Millis(time.Date(2026, 1, 2, 3, 4, 5, 600_000_000, time.UTC))
	if got != "2026-01-02T03:04:05.600Z" {
		t.Fatalf("got %q", got)
	}
}
