package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kaikei-e/kisage/cli"
	"github.com/Kaikei-e/kisage/host"
)

func TestHTTPResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusNoContent)
		case "/redirect":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/slow":
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	for _, tc := range []struct {
		name, path   string
		status, code int
		want         string
	}{
		{"healthy", "/ok", 204, 0, "1/1 healthy"},
		{"unhealthy", "/bad", 200, 1, "HTTP 503, want 200"},
		{"redirect is observed", "/redirect", 204, 1, "HTTP 302, want 204"},
		{"redirect can be expected", "/redirect", 302, 0, "1/1 healthy"},
		{"timeout", "/slow", 200, 1, "0/1 healthy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			cfg := config{timeout: 100 * time.Millisecond, checks: []endpoint{{"api", server.URL + tc.path, tc.status}}}
			if code := check(cfg, &out); code != tc.code || !strings.Contains(out.String(), tc.want) {
				t.Fatalf("code=%d, output=%s", code, &out)
			}
		})
	}
	server.Close()
	var out bytes.Buffer
	if code := check(config{timeout: time.Second, checks: []endpoint{{"offline", server.URL, 200}}}, &out); code != 1 || !strings.Contains(out.String(), "FAIL offline") {
		t.Fatalf("connection failure: code=%d output=%s", code, &out)
	}
}

func TestInvalidConfiguration(t *testing.T) {
	valid := "@lkal \"0.1\"\nlocal {\n timeout_ms = 2000\n}\nchecks {\n api {\n url = \"http://localhost/health\"\n status = 200\n }\n}\n"
	for _, tc := range []struct{ old, replacement string }{
		{"timeout_ms = 2000", "timeout_ms = 0"},
		{"timeout_ms = 2000", "timeout_ms = 60001"},
		{"timeout_ms = 2000", "timeout_ms = \"2000\""},
		{"http://localhost/health", "file:///etc/passwd"},
		{"http://localhost/health", "/health"},
		{"status = 200", "status = 600"},
		{"status = 200", "status = \"200\""},
		{"status = 200", "unknown = 200"},
	} {
		t.Run(tc.replacement, func(t *testing.T) {
			if _, err := parseConfig("test", []byte(strings.ReplaceAll(valid, tc.old, tc.replacement))); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}
	if _, err := parseConfig("test", []byte(valid)); err != nil {
		t.Fatal(err)
	}
	raw, _ := templates.ReadFile("config.lkal")
	if _, err := parseConfig("unapplied", raw); err == nil {
		t.Fatal("an unapplied configuration must not report success")
	}
}

// Exercise the example through the real file store and CLI runner, reopening
// the host between calls just as separate kisage processes do.
func TestKisageLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/unhealthy" {
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	root := filepath.Join(t.TempDir(), "sandbox")
	if err := initialize(root, io.Discard); err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(root, "host.json")
	target := filepath.Join(root, "config.lkal")
	declaration := filepath.Join(root, "desired.lkal")
	read := func(path string) string {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	write := func(path, raw string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runKisage := func(want int, args ...string) string {
		t.Helper()
		runtime, err := host.Open(policy)
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Close()
		var out, stderr bytes.Buffer
		if code := cli.NewRunner(runtime.Host).Run(args, &out, &stderr); code != want {
			t.Fatalf("kisage %v: code=%d, want=%d\n%s\n%s", args, code, want, &out, &stderr)
		}
		return out.String()
	}
	initial := read(target)
	if err := initialize(root, io.Discard); err == nil || read(target) != initial {
		t.Fatal("initialization must refuse to overwrite an existing sandbox")
	}
	desired := strings.ReplaceAll(read(declaration), "http://127.0.0.1:18080", server.URL)
	write(declaration, desired)
	runKisage(2, "plan", declaration, "-detailed-exitcode")
	if read(target) != initial {
		t.Fatal("plan changed the target")
	}
	runKisage(0, "apply", declaration)
	var out, stderr bytes.Buffer
	if code := run([]string{"-config", target}, &out, &stderr); code != 1 || !strings.Contains(out.String(), "1/2 healthy") {
		t.Fatalf("mixed health: code=%d out=%s err=%s", code, &out, &stderr)
	}
	converged := read(target)
	runKisage(0, "plan", declaration, "-detailed-exitcode")
	runKisage(0, "apply", declaration)
	if read(target) != converged {
		t.Fatal("repeated apply changed the target")
	}
	// A local preference is outside the declared subtree and must survive updates.
	write(target, strings.ReplaceAll(converged, "timeout_ms = 2000", "timeout_ms = 1500"))
	desired = strings.ReplaceAll(desired, "status = 200", "status = 503")
	// Keep the healthy endpoint's expected status at 200.
	desired = strings.Replace(desired, "status = 503", "status = 200", 1)
	write(declaration, desired)
	runKisage(0, "apply", declaration)
	out.Reset()
	if code := run([]string{"-config", target}, &out, &stderr); code != 0 || !strings.Contains(out.String(), "2/2 healthy") {
		t.Fatalf("updated expectations: code=%d output=%s", code, &out)
	}
	if !strings.Contains(read(target), "timeout_ms = 1500") || !strings.Contains(read(target), "Keep this comment too.") {
		t.Fatal("local settings or their comment were lost")
	}
	// Retiring a Created endpoint requires a separate prune operation.
	desired = fmt.Sprintf("@lkal \"0.1\"\nchecks {\n api {\n url = %q\n status = 200\n }\n}\n", server.URL+"/health")
	write(declaration, desired)
	runKisage(0, "apply", declaration)
	if !strings.Contains(read(target), "/unhealthy") {
		t.Fatal("apply deleted a retired endpoint")
	}
	runKisage(0, "prune", declaration)
	if strings.Contains(read(target), "/unhealthy") {
		t.Fatal("prune retained a retired Created endpoint")
	}
	// The protected host policy rejects an attempt to manage local preferences.
	before := read(target)
	write(declaration, desired+"local {\n label = \"out-of-scope\"\n}\n")
	if output := runKisage(1, "apply", declaration); !strings.Contains(output, "K401") || read(target) != before {
		t.Fatalf("scope denial must preserve the target: %s", output)
	}
	// External drift blocks even an otherwise permitted sibling status update.
	write(target, strings.ReplaceAll(before, "/health", "/manual"))
	write(declaration, strings.ReplaceAll(desired, "status = 200", "status = 204"))
	drifted := read(target)
	if output := runKisage(1, "apply", declaration); !strings.Contains(output, "K203") || read(target) != drifted {
		t.Fatalf("drift must block all environment writes: %s", output)
	}
}
