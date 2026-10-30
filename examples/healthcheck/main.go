// healthcheck checks HTTP endpoints configured by kisage.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	lkal "github.com/Kaikei-e/LKAL"
)

type endpoint struct {
	name   string
	url    string
	status int
}

type config struct {
	timeout time.Duration
	checks  []endpoint
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "init" {
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: healthcheck init NEW_DIRECTORY")
			return 2
		}
		if err := initialize(args[1], stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		return 0
	}
	flags := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", "config.lkal", "runtime LKAL configuration")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: healthcheck [-config FILE]\n       healthcheck init NEW_DIRECTORY")
	}
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		flags.Usage()
		return 2
	}
	raw, err := readBoundedConfigFile(*path, 1<<20)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	cfg, err := parseConfig(*path, raw)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return check(cfg, stdout)
}

func readBoundedConfigFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("config %q must be a regular file", path)
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("config %q exceeds maximum allowed size (%d bytes)", path, maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("config %q exceeds maximum allowed size (%d bytes)", path, maxBytes)
	}
	return data, nil
}

// Validate application policy before making any requests. kisage itself only
// reconciles values; URL, status and timeout rules belong to the consumer.
func parseConfig(name string, raw []byte) (config, error) {
	f, err := lkal.Parse(name, raw)
	if err != nil {
		return config{}, err
	}
	timeoutValue, err := f.Get("local/timeout_ms")
	timeout, ok := timeoutValue.(lkal.Int)
	if err != nil || !ok || timeout < 1 || timeout > 60000 {
		return config{}, fmt.Errorf("local/timeout_ms must be an integer from 1 to 60000")
	}
	value, err := f.Get("checks")
	record, ok := value.(lkal.Record)
	if err != nil || !ok || len(record.Fields) == 0 {
		return config{}, fmt.Errorf("checks must contain at least one endpoint; apply desired.lkal with kisage first")
	}
	cfg := config{timeout: time.Duration(timeout) * time.Millisecond}
	for _, field := range record.Fields {
		rec, ok := field.Value.(lkal.Record)
		if !ok {
			return config{}, fmt.Errorf("checks/%s must be a record with url and status", field.Key)
		}
		var target lkal.String
		var status lkal.Int
		for _, setting := range rec.Fields {
			switch setting.Key {
			case "url":
				target, _ = setting.Value.(lkal.String)
			case "status":
				status, _ = setting.Value.(lkal.Int)
			default:
				return config{}, fmt.Errorf("checks/%s: unknown setting %q", field.Key, setting.Key)
			}
		}
		u, err := url.Parse(string(target))
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return config{}, fmt.Errorf("checks/%s/url must be an absolute HTTP(S) URL", field.Key)
		}
		if status < 100 || status > 599 {
			return config{}, fmt.Errorf("checks/%s/status must be an integer from 100 to 599", field.Key)
		}
		cfg.checks = append(cfg.checks, endpoint{field.Key, string(target), int(status)})
	}
	return cfg, nil
}

func check(cfg config, stdout io.Writer) int {
	client := &http.Client{
		Timeout: cfg.timeout,
		// Inspect the configured endpoint's status, including redirects.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	failed := 0
	for _, ep := range cfg.checks {
		start := time.Now()
		resp, err := client.Get(ep.url)
		elapsed := time.Since(start).Round(time.Millisecond)
		if err != nil {
			failed++
			fmt.Fprintf(stdout, "FAIL %-16s %s (%s)\n", sanitizeOutput(ep.name), sanitizeOutput(err.Error()), elapsed)
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode != ep.status {
			failed++
			fmt.Fprintf(stdout, "FAIL %-16s HTTP %d, want %d (%s)\n", sanitizeOutput(ep.name), resp.StatusCode, ep.status, elapsed)
		} else {
			fmt.Fprintf(stdout, "OK   %-16s HTTP %d (%s)\n", sanitizeOutput(ep.name), resp.StatusCode, elapsed)
		}
	}
	fmt.Fprintf(stdout, "%d/%d healthy\n", len(cfg.checks)-failed, len(cfg.checks))
	if failed > 0 {
		return 1
	}
	return 0
}

func sanitizeOutput(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			out = append(out, '?')
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
