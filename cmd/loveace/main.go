// Command loveace is the CLI for the LoveACE Go core.
//
// Contract (mirrors the Rust loveace CLI):
//   - stdout carries exactly one JSON envelope per command
//   - stderr carries prompts and diagnostics
//   - credentials never appear in arguments, logs or envelopes
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/diagnostics"
	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/ops"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/timeutil"
	"github.com/Sibuxiangx/loveace-go/core/transport"

	"golang.org/x/term"
)

// Envelope schema version (Rust CLI parity).
const schemaVersion = 1

// Process exit codes.
const (
	exitOK          = 0
	exitInvocation  = 2
	exitAuthNetwork = 3
	exitBusiness    = 4
)

type envelope struct {
	SchemaVersion int             `json:"schema_version"`
	Operation     string          `json:"operation"`
	RequestID     string          `json:"request_id"`
	OK            bool            `json:"ok"`
	Data          json.RawMessage `json:"data,omitempty"`
	Error         *errorPayload   `json:"error,omitempty"`
	Timing        timing          `json:"timing"`
}

type errorPayload struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type timing struct {
	StartedAtMS int64 `json:"started_at_ms"`
	DurationMS  int64 `json:"duration_ms"`
}

var reqSeq int64

func requestID() string {
	reqSeq++
	return fmt.Sprintf("%08x%04x", timeutil.NowMillis()&0xffff_ffff, reqSeq&0xffff)
}

func emit(env *envelope) {
	line, err := json.Marshal(env)
	if err != nil {
		line = []byte(`{"schema_version":1,"ok":false,"error":{"code":"error","message":"serialization failed","retryable":false}}`)
	}
	fmt.Fprintln(os.Stdout, string(line))
}

func emitError(operation string, startedAt int64, err error) *envelope {
	env := &envelope{
		SchemaVersion: schemaVersion,
		Operation:     operation,
		RequestID:     requestID(),
		OK:            false,
		Timing: timing{
			StartedAtMS: startedAt,
			DurationMS:  timeutil.NowMillis() - startedAt,
		},
	}
	var core *errs.Error
	if asErr(err, &core) {
		env.Error = &errorPayload{
			Code:      core.Code.String(),
			Message:   core.Error(),
			Retryable: core.Retryable(),
		}
	} else {
		env.Error = &errorPayload{Code: "error", Message: err.Error()}
	}
	emit(env)
	return env
}

func emitSuccess(operation string, startedAt int64, data any) *envelope {
	raw, err := json.Marshal(data)
	if err != nil {
		return emitError(operation, startedAt, err)
	}
	env := &envelope{
		SchemaVersion: schemaVersion,
		Operation:     operation,
		RequestID:     requestID(),
		OK:            true,
		Data:          raw,
		Timing: timing{
			StartedAtMS: startedAt,
			DurationMS:  timeutil.NowMillis() - startedAt,
		},
	}
	emit(env)
	return env
}

func exitCodeFor(err error) int {
	if errs.IsInvocation(err) {
		return exitInvocation
	}
	if errs.IsAuthOrNetwork(err) {
		return exitAuthNetwork
	}
	return exitBusiness
}

func asErr(err error, target **errs.Error) bool {
	e, ok := err.(*errs.Error)
	if ok {
		*target = e
		return true
	}
	return false
}

// ---- credentials ----

type credentials struct {
	Account         string
	Password        string
	GatewayPassword string
}

// loadEnvFile parses KEY=VALUE lines into the environment (first '=' splits;
// the remainder is taken verbatim: no quotes, no escapes).
func loadEnvFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		idx := strings.Index(trimmed, "=")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:idx])
		value := trimmed[idx+1:]
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, value)
		}
	}
	return nil
}

// resolveCredentials precedence: stdin JSON > env vars > TTY prompts.
// nonInteractive forbids prompting: missing values return a useful error.
func resolveCredentials(stdinCredentials, nonInteractive bool) (*credentials, error) {
	account := os.Getenv("LOVEACE_ACCOUNT")
	password := os.Getenv("LOVEACE_PASSWORD")
	gatewayPassword := os.Getenv("LOVEACE_GATEWAY_PASSWORD")

	if stdinCredentials {
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return nil, &errs.Error{Code: errs.Other, Msg: fmt.Sprintf("failed to read credentials from stdin: %v", err)}
		}
		var parsed struct {
			Account         *string `json:"account"`
			Password        *string `json:"password"`
			GatewayPassword *string `json:"gateway_password"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &parsed); err != nil {
			return nil, errs.NewInvalidArgument("invalid stdin credential JSON: " + err.Error())
		}
		if parsed.Account != nil {
			account = *parsed.Account
		}
		if parsed.Password != nil {
			password = *parsed.Password
		}
		if parsed.GatewayPassword != nil {
			gatewayPassword = *parsed.GatewayPassword
		}
	}

	if account == "" {
		if nonInteractive {
			return nil, errs.NewInvalidArgument("missing account id; provide --account, --env-file or stdin credential JSON")
		}
		account = promptLine("Account (student id): ")
	}
	if password == "" {
		if nonInteractive {
			return nil, errs.NewInvalidArgument("missing password; provide stdin credential JSON or LOVEACE_PASSWORD")
		}
		password = promptPassword("School password: ")
	}
	if gatewayPassword == "" {
		if nonInteractive {
			// Default gateway password to the school password, matching the
			// existing apps.
			gatewayPassword = password
		} else {
			fmt.Fprint(os.Stderr, "Gateway password (leave blank to use school password): ")
			gatewayPassword = promptPassword("")
			if gatewayPassword == "" {
				gatewayPassword = password
			}
		}
	}

	creds := &credentials{
		Account:         account,
		Password:        password,
		GatewayPassword: gatewayPassword,
	}
	if err := session.ValidateCredentials(creds.Account, creds.GatewayPassword, creds.Password); err != nil {
		return nil, err
	}
	return creds, nil
}

func promptLine(prompt string) string {
	fmt.Fprint(os.Stderr, prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line)
}

func promptPassword(prompt string) string {
	if prompt != "" {
		fmt.Fprint(os.Stderr, prompt)
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		return strings.TrimRight(line, "\r\n")
	}
	data, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return ""
	}
	return string(data)
}

// ---- commands ----

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) < 2 {
		usage()
		return exitInvocation
	}
	switch os.Args[1] {
	case "version", "--version", "-V":
		fmt.Printf("loveace-go %s (core %s)\n", "0.1.0", diagnostics.CoreVersion)
		return exitOK
	case "operations":
		return cmdOperations(os.Args[2:])
	case "call":
		return cmdCall(os.Args[2:])
	case "shell":
		return cmdShell(os.Args[2:])
	case "self-test":
		return cmdSelfTest(os.Args[2:])
	case "bench":
		return cmdBench(os.Args[2:])
	case "help", "--help", "-h":
		usage()
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		return exitInvocation
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `loveace — LoveACE Go core CLI

Usage:
  loveace version
  loveace operations [--json]
  loveace call <operation> [--params JSON] [--account ID] [--allow-write]
               [--stdin-credentials] [--non-interactive] [--env-file PATH]
  loveace shell [--account ID] [--allow-write] [--env-file PATH]
  loveace self-test [--json]

Credentials come from stdin JSON, environment (LOVEACE_ACCOUNT /
LOVEACE_PASSWORD / LOVEACE_GATEWAY_PASSWORD) or interactive prompts.
Passwords are never passed as CLI arguments.
`)
}

func cmdOperations(args []string) int {
	fs := flag.NewFlagSet("operations", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.Parse(args)
	started := timeutil.NowMillis()
	type opInfo struct {
		Name         string        `json:"name"`
		Summary      string        `json:"summary"`
		RequiresAuth bool          `json:"requires_auth"`
		Mutates      bool          `json:"mutates"`
		Args         []ops.ArgSpec `json:"args"`
	}
	catalog := ops.Catalog()
	infos := make([]opInfo, 0, len(catalog))
	for _, spec := range catalog {
		infos = append(infos, opInfo{
			Name: spec.Name, Summary: spec.Summary,
			RequiresAuth: spec.RequiresAuth, Mutates: spec.Mutates, Args: spec.Args,
		})
	}
	emitSuccess("operations", started, infos)
	return exitOK
}

type commonFlags struct {
	params           string
	account          string
	allowWrite       bool
	stdinCredentials bool
	nonInteractive   bool
	envFile          string
}

func addCommonFlags(fs *flag.FlagSet, cf *commonFlags) {
	fs.StringVar(&cf.params, "params", "", "JSON object of operation parameters")
	fs.StringVar(&cf.account, "account", "", "student account id")
	fs.BoolVar(&cf.allowWrite, "allow-write", false, "permit mutating operations")
	fs.BoolVar(&cf.stdinCredentials, "stdin-credentials", false, "read credentials JSON from stdin")
	fs.BoolVar(&cf.nonInteractive, "non-interactive", false, "never prompt; error on missing credentials")
	fs.StringVar(&cf.envFile, "env-file", "", "load KEY=VALUE pairs from a file")
}

func buildSession(cf *commonFlags) (*session.Session, *credentials, error) {
	if cf.envFile != "" {
		if err := loadEnvFile(cf.envFile); err != nil {
			return nil, nil, errs.NewInvalidArgument("env-file: " + err.Error())
		}
	}
	if cf.account != "" {
		os.Setenv("LOVEACE_ACCOUNT", cf.account)
	}
	creds, err := resolveCredentials(cf.stdinCredentials, cf.nonInteractive)
	if err != nil {
		return nil, nil, err
	}
	cfg := session.DefaultConfig()
	cfg.AllowWrites = cf.allowWrite
	tr := transport.NewHTTPTransport(0)
	s := session.NewWithConfig(creds.Account, creds.GatewayPassword, creds.Password, tr, cfg)
	return s, creds, nil
}

func cmdCall(args []string) int {
	fs := flag.NewFlagSet("call", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var cf commonFlags
	addCommonFlags(fs, &cf)
	// Go's flag package stops at the first non-flag argument, so extract the
	// operation name (the single positional) up front and parse the rest.
	var operation string
	var remaining []string
	for _, arg := range args {
		if operation == "" && !strings.HasPrefix(arg, "-") {
			operation = arg
			continue
		}
		remaining = append(remaining, arg)
	}
	if err := fs.Parse(remaining); err != nil {
		return exitInvocation
	}
	if operation == "" {
		fmt.Fprintln(os.Stderr, "call requires an operation name")
		return exitInvocation
	}
	started := timeutil.NowMillis()

	spec, ok := ops.Find(operation)
	if !ok {
		emitError(operation, started, errs.NewNotImplemented("unknown operation: "+operation))
		return exitInvocation
	}
	if spec.Mutates && !cf.allowWrite {
		err := errs.NewInvalidArgument("write operation requires explicit --allow-write permission")
		emitError(operation, started, err)
		return exitInvocation
	}

	s, _, err := buildSession(&cf)
	if err != nil {
		emitError(operation, started, err)
		return exitCodeFor(err)
	}

	var params json.RawMessage = json.RawMessage(`{}`)
	if cf.params != "" {
		if !json.Valid([]byte(cf.params)) {
			emitError(operation, started, errs.NewInvalidArgument("--params must be valid JSON"))
			return exitInvocation
		}
		params = json.RawMessage(cf.params)
	}

	data, err := ops.Dispatch(context.Background(), s, operation, params)
	if err != nil {
		emitError(operation, started, err)
		return exitCodeFor(err)
	}
	emitSuccess(operation, started, data)
	return exitOK
}

func cmdShell(args []string) int {
	fs := flag.NewFlagSet("shell", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var cf commonFlags
	addCommonFlags(fs, &cf)
	// The shell reads commands from stdin, so credentials must come from the
	// environment (or prompts before the loop starts) — never stdin JSON.
	if err := fs.Parse(args); err != nil {
		return exitInvocation
	}
	started := timeutil.NowMillis()

	s, _, err := buildSession(&cf)
	if err != nil {
		emitError("shell", started, err)
		return exitCodeFor(err)
	}

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Fprintln(os.Stderr, "loveace shell — one operation per line: <operation> [json-params]; 'quit' exits")
	for {
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "quit" || line == "exit" {
			break
		}
		parts := strings.SplitN(line, " ", 2)
		operation := parts[0]
		params := json.RawMessage(`{}`)
		if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
			params = json.RawMessage(strings.TrimSpace(parts[1]))
			if !json.Valid(params) {
				emitError(operation, timeutil.NowMillis(), errs.NewInvalidArgument("params must be valid JSON"))
				continue
			}
		}
		opStarted := timeutil.NowMillis()
		spec, ok := ops.Find(operation)
		if !ok {
			emitError(operation, opStarted, errs.NewNotImplemented("unknown operation: "+operation))
			continue
		}
		if spec.Mutates && !cf.allowWrite {
			emitError(operation, opStarted, errs.NewInvalidArgument("write operation requires explicit --allow-write permission"))
			continue
		}
		data, err := ops.Dispatch(context.Background(), s, operation, params)
		if err != nil && errs.CodeOf(err) == errs.InteractionRequired {
			// The gateway demanded a CAPTCHA: fetch image → prompt →
			// submit → finish login, then retry the operation once.
			if cerr := captchaLoginLoop(s, func(label string) string {
				fmt.Fprint(os.Stderr, label)
				if !scanner.Scan() {
					return ""
				}
				return scanner.Text()
			}); cerr != nil {
				emitError(operation, opStarted, cerr)
				continue
			}
			data, err = ops.Dispatch(context.Background(), s, operation, params)
		}
		if err != nil {
			emitError(operation, opStarted, err)
			continue
		}
		emitSuccess(operation, opStarted, data)
	}
	return exitOK
}

// captchaLoginLoop drives the interactive CAPTCHA flow (§8 item 3):
// fetch the image, save it as a 0600 temp file, prompt for the code,
// submit; a rejected code loops (max 3 attempts). The gif is bound to the
// auth context's TWFID, so the loop must complete within one challenge.
func captchaLoginLoop(s *session.Session, prompt func(string) string) error {
	ctx := context.Background()
	for attempt := 0; attempt < 3; attempt++ {
		captcha, err := s.EcCaptcha(ctx)
		if err != nil {
			return err
		}
		if !captcha.Required || len(captcha.Image) == 0 {
			// Pollution cleared while waiting: a plain retry suffices.
			return nil
		}
		path := filepath.Join(os.TempDir(), fmt.Sprintf("loveace-captcha-%d.gif", time.Now().UnixNano()))
		if werr := os.WriteFile(path, captcha.Image, 0o600); werr != nil {
			return werr
		}
		fmt.Fprintf(os.Stderr, "学校要求验证码；图片已保存: %s （看完可直接删除）\n", path)
		code := strings.TrimSpace(prompt("请输入验证码: "))
		if code == "" {
			return errs.NewCancelled()
		}
		st := s.ECLoginWithCaptcha(ctx, code)
		switch st.Outcome {
		case session.ECSuccess:
			return s.CompleteCaptchaLogin(ctx)
		case session.ECCaptchaInvalid:
			fmt.Fprintln(os.Stderr, "验证码不对，重新拉图。")
			continue
		default:
			return fmt.Errorf("captcha login failed: %s", st.Outcome)
		}
	}
	return errs.NewInteractionRequired("验证码重试次数用尽")
}

func cmdSelfTest(args []string) int {
	fs := flag.NewFlagSet("self-test", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.Parse(args)
	started := timeutil.NowMillis()

	// Synthetic self-test: no school connection, no real credentials. Runs a
	// login + a business call against an in-memory mock transport.
	tr := testutilLoginTransport()
	cfg := session.DefaultConfig()
	s := session.NewWithConfig("selftest", "gw", "school", tr, cfg)

	report := map[string]any{
		"synthetic": true,
		"checks":    []map[string]any{},
	}
	checks := report["checks"].([]map[string]any)
	addCheck := func(name string, ok bool, detail string) {
		checks = append(checks, map[string]any{"name": name, "ok": ok, "detail": detail})
		report["checks"] = checks
	}

	if err := s.Login(context.Background()); err != nil {
		addCheck("login", false, err.Error())
	} else {
		addCheck("login", true, "mock EC+UAAP login")
	}
	if _, err := ops.Dispatch(context.Background(), s, "jwc.academic_info",
		json.RawMessage(`{}`)); err != nil {
		addCheck("dispatch.jwc.academic_info", false, err.Error())
	} else {
		addCheck("dispatch.jwc.academic_info", true, "mock dispatch")
	}
	if _, err := ops.Dispatch(context.Background(), s, "session.clear_cookies",
		json.RawMessage(`{"scope":"all"}`)); err != nil {
		addCheck("session.clear_cookies", false, err.Error())
	} else {
		addCheck("session.clear_cookies", true, "cookie scope clear")
	}
	if err := s.EnsureAuthenticated(context.Background()); err != nil {
		addCheck("recovery", false, err.Error())
	} else {
		addCheck("recovery", true, "recovery after cookie clear")
	}
	s.Logout()
	addCheck("logout", s.State() == session.StateSignedOut, "state machine")

	allOK := true
	for _, c := range checks {
		if ok, _ := c["ok"].(bool); !ok {
			allOK = false
		}
	}
	report["ok"] = allOK
	emitSuccess("self-test", started, report)
	if allOK {
		return exitOK
	}
	return exitBusiness
}

func testutilLoginTransport() *transport.MockTransport {
	// Inline login fixture (avoids importing the internal test package).
	modulus := "c8a2029acbea3b4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f7a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718b"
	authBody := "<auth><TwfID>t1</TwfID><RSA_ENCRYPT_KEY>" + modulus +
		"</RSA_ENCRYPT_KEY><RSA_ENCRYPT_EXP>65537</RSA_ENCRYPT_EXP><CSRF_RAND_CODE>c1</CSRF_RAND_CODE></auth>"
	casPage := `<form action="/cas/login" method="post">
		<input type="hidden" name="lt" value="LT-1"/>
		<input type="hidden" name="execution" value="E-1"/></form>`
	return transport.NewMockTransport().
		OnRequest(transport.Get, "/por/login_auth.csp", transport.NewResponse(200, []byte(authBody))).
		OnRequest(transport.Post, "/por/login_psw.csp", transport.NewResponse(200, []byte("<Result>1</Result>"))).
		OnRequest(transport.Get, "j_spring_cas_security_check?ticket=", transport.NewResponse(200, []byte("<html>jwc</html>"))).
		OnRequest(transport.Get, "cas/login", transport.NewResponse(200, []byte(casPage))).
		OnRequest(transport.Post, "cas/login", transport.NewResponse(302, []byte("The URL has moved")).
			WithHeader("Location", "http://jwcxk2-aufe-edu-cn.vpn2.aufe.edu.cn:8118/j_spring_cas_security_check?ticket=ST1")).
		OnRequest(transport.Post, "/main/academicInfo", transport.NewResponse(200, []byte(
			`[{"courseNum":1,"coursePas":0,"gpa":4.0,"courseNum_bxqyxd":0,"zxjxjhh":"2024-2025-1-1"}]`)))
}
