package diagnostics

import (
	"strings"
	"testing"
)

func TestRedactsSensitiveNames(t *testing.T) {
	var r Redactor
	if !r.IsSensitive("Set-Cookie") || !r.IsSensitive("TWFID") || !r.IsSensitive("lt") {
		t.Fatal("sensitive names must be detected")
	}
	if r.IsSensitive("Content-Type") {
		t.Fatal("Content-Type must not be sensitive")
	}
}

func TestRedactsURLQuery(t *testing.T) {
	var r Redactor
	out := r.URL("https://x/a?password=hunter2&k=v")
	if !strings.Contains(out, "password=<redacted>") {
		t.Fatalf("out = %q", out)
	}
	if strings.Contains(out, "hunter2") {
		t.Fatalf("secret leaked: %q", out)
	}
	if !strings.Contains(out, "k=v") {
		t.Fatalf("out = %q", out)
	}
	if got := r.URL(""); got != "unknown" {
		t.Fatalf("empty URL = %q", got)
	}
}

func TestRedactsURLKeepsPortAndPath(t *testing.T) {
	var r Redactor
	out := r.URL("http://jwcxk2-aufe-edu-cn.vpn2.aufe.edu.cn:8118/main/academicInfo?sf_request_type=ajax&ticket=ST-1")
	if !strings.Contains(out, ":8118/main/academicInfo") {
		t.Fatalf("out = %q", out)
	}
	if !strings.Contains(out, "sf_request_type=ajax") {
		t.Fatalf("out = %q", out)
	}
	if strings.Contains(out, "ST-1") {
		t.Fatalf("ticket leaked: %q", out)
	}
	if !strings.Contains(out, "ticket=<redacted>") {
		t.Fatalf("out = %q", out)
	}
}

func TestRedactsTextFields(t *testing.T) {
	var r Redactor
	out := r.Text("svpn_password=secret123&svpn_name=20000002")
	if strings.Contains(out, "secret123") {
		t.Fatalf("out = %q", out)
	}
	xml := r.Text("<TwfID>abc</TwfID>")
	if !strings.Contains(xml, "<redacted>") || strings.Contains(xml, "abc") {
		t.Fatalf("xml = %q", xml)
	}
}

func TestBufferIsBounded(t *testing.T) {
	collector := NewCollector(Options{VerboseHTTP: false, MaxBufferBytes: 400})
	for i := 0; i < 50; i++ {
		collector.Record(DiagnosticEvent{
			Event:     "request",
			Operation: "op",
			Phase:     "read",
			ElapsedMS: 1,
			Outcome:   "ok",
		})
	}
	if collector.Len() >= 50 {
		t.Fatalf("buffer not bounded: %d", collector.Len())
	}
}

func TestBuildReport(t *testing.T) {
	collector := NewCollector(DefaultOptions())
	collector.Record(DiagnosticEvent{Event: "request", Operation: "op", Phase: "read", Outcome: "ok"})
	report := collector.BuildReport("r1", IncidentFingerprint("jwc", "read", "timeout", "none", "v1"), "summary")
	if report.Manifest.ReportID != "r1" || report.Manifest.SchemaVersion != 1 {
		t.Fatalf("manifest = %+v", report.Manifest)
	}
	if len(report.Timeline) != 1 {
		t.Fatalf("timeline = %+v", report.Timeline)
	}
	if report.Environment["core_version"] == "" {
		t.Fatal("environment missing core_version")
	}
}
