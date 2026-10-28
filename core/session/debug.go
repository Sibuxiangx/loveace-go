package session

import (
	"fmt"
	"net/url"
)

// DebugHook, when set, receives session-internal debug traces (the mint
// walk's hop verdicts, recovery steps). The server wires it to its logger;
// nil (the default) disables tracing entirely. Implementations MUST NOT
// log cookie values, tickets or credentials — only hosts, paths, statuses.
var DebugHook func(format string, args ...any)

func debugf(format string, args ...any) {
	if DebugHook != nil {
		DebugHook("session: " + fmt.Sprintf(format, args...))
	}
}

// hostOf reduces a URL to scheme://host/path for safe debug logging.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "unparseable"
	}
	return u.Scheme + "://" + u.Host + u.Path
}
