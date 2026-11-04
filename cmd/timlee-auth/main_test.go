package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain lets a test run the real main in a child process. Trusting the
// proxy and joining readiness to the drain happen only in main, so they are
// tested as they ship rather than as a harness would re-create them.
func TestMain(m *testing.M) {
	if os.Getenv("TIMLEE_AUTH_RUN_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

func TestVersionNeedsNoConfiguration(t *testing.T) {
	// Production with none of its secrets: anything that reached config
	// validation would exit 1 instead of answering.
	cmd := exec.Command(os.Args[0], "--version")
	cmd.Env = []string{"TIMLEE_AUTH_RUN_MAIN=1", "NODE_ENV=production"}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "timlee-auth ") {
		t.Fatalf("--version: err %v, output %q; want the binary's name and build", err, out)
	}
}

func TestProductionWiring(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the service and waits out its drain delay")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()
	base := "http://" + addr

	var output bytes.Buffer
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Stdout, cmd.Stderr = &output, &output
	// A complete environment rather than the inherited one, so nothing set in
	// the developer's shell changes what is being tested.
	cmd.Env = []string{
		"TIMLEE_AUTH_RUN_MAIN=1",
		"NODE_ENV=production",
		"TIMLEE_CONTROL_ADDR=" + addr,
		"TIMLEE_DB=:memory:",
		"SESSION_SECRET=session-secret-for-tests-0123456789abcdef",
		"TIMLEE_INTERNAL_TOKEN=internal-token-for-tests-0123456789abcdef",
		"TIMLEE_PUBLIC_ORIGIN=https://timlee.test",
		"TIMLEE_CONTROL_ORIGIN=https://auth.timlee.test",
		"TIMLEE_PREVIEW_ORIGIN=https://preview.timlee-apps.test",
		"TIMLEE_COPILOT_MAX_AI_CREDITS=5",
		"TIMLEE_API_ORIGIN=https://api.timlee.test",
		"TIMLEE_TRUST_PROXY=true",
		"GITHUB_CLIENT_ID=test-client",
		"GITHUB_CLIENT_SECRET=test-secret",
		"LOG_LEVEL=warn",
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
		if t.Failed() {
			t.Logf("service output:\n%s", output.String())
		}
	})

	client := &http.Client{
		Timeout:       2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	readiness := func() (status int, kind string) {
		res, err := client.Get(base + "/readyz")
		if err != nil {
			return 0, err.Error()
		}
		defer res.Body.Close()
		var body struct {
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		_ = json.NewDecoder(res.Body).Decode(&body)
		return res.StatusCode, body.Error.Type
	}
	waitFor := func(want int, within time.Duration) (int, string) {
		deadline := time.Now().Add(within)
		for {
			status, kind := readiness()
			if status == want || time.Now().After(deadline) {
				return status, kind
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	if status, detail := waitFor(http.StatusOK, 15*time.Second); status != http.StatusOK {
		t.Fatalf("/readyz never became ready: %d %s", status, detail)
	}

	// The edge terminates TLS and says so. Believing it is what lets a
	// production sign-in set its Secure cookie at all.
	start, err := http.NewRequest(http.MethodGet, base+"/auth/github/start", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	start.Header.Set("X-Forwarded-Proto", "https")
	res, err := client.Do(start)
	if err != nil {
		t.Fatalf("start sign-in: %v", err)
	}
	res.Body.Close()
	if cookies := res.Cookies(); res.StatusCode != http.StatusFound || len(cookies) != 1 || !cookies[0].Secure {
		t.Errorf("sign-in behind a trusted proxy: status %d with %d cookies, want 302 and one Secure cookie",
			res.StatusCode, len(cookies))
	}

	// The edge learns of a shutdown from /readyz, so it has to turn 503 while
	// the listener is still open to say so.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	if status, kind := waitFor(http.StatusServiceUnavailable, 3*time.Second); status != http.StatusServiceUnavailable || kind != "draining" {
		t.Fatalf("/readyz after SIGTERM: %d %q, want 503 draining while still listening", status, kind)
	}

	select {
	case err := <-exited:
		exited <- err
		if err != nil {
			t.Errorf("exit after draining: %v, want a clean exit", err)
		}
	case <-time.After(25 * time.Second):
		t.Error("still running 25s after SIGTERM")
	}
}
