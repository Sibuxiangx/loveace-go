package transport

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// gbkDecoder decodes the legacy GBK family charsets.
var gbkDecoder = simplifiedchinese.GBK

// netDialer wraps net.Dialer for transport configuration.
type netDialer = net.Dialer

func isCanonical(name string) bool {
	for _, r := range name {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '-' {
			continue
		}
		return false
	}
	return name != "" && unicode.IsUpper(rune(name[0]))
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if u.Port() != "" {
		return u.Host
	}
	return u.Hostname()
}

func parseI64(s string) (int64, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return v, err == nil
}
