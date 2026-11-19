package host_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Kaikei-e/kisage/cli"
	"github.com/Kaikei-e/kisage/host"
)

func TestLoadConfigMissingReportsPathAndEnvHint(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	realTarget := filepath.Join(dir, "real-host.json")
	if err := os.WriteFile(realTarget, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(dir, "symlink-host.json")
	if err := os.Symlink(realTarget, symlinkPath); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name         string
		path         string
		wantUsageErr bool
		wantENOENT   bool
		wantSubstr   string
	}{
		{
			name:         "missing-parent-dir",
			path:         filepath.Join(dir, "missing", "host.json"),
			wantUsageErr: false,
			wantENOENT:   true,
			wantSubstr:   syscall.ENOENT.Error(),
		},
		{
			name:         "missing-file",
			path:         filepath.Join(dir, "missing-host.json"),
			wantUsageErr: true,
			wantENOENT:   true,
			wantSubstr:   syscall.ENOENT.Error(),
		},
		{
			name:         "symlink",
			path:         symlinkPath,
			wantUsageErr: true,
			wantENOENT:   false,
			wantSubstr:   "symbolic link",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := host.LoadConfig(tc.path)
			if err == nil {
				t.Fatal("expected error for missing or invalid host configuration")
			}
			if tc.wantENOENT && !errors.Is(err, syscall.ENOENT) {
				t.Fatalf("expected errors.Is(err, syscall.ENOENT), got %v", err)
			}
			if got := cli.IsUsageError(err); got != tc.wantUsageErr {
				t.Fatalf("cli.IsUsageError(%v) = %v, want %v", err, got, tc.wantUsageErr)
			}
			for _, want := range []string{
				"host configuration",
				tc.path,
				tc.wantSubstr,
				"KISAGE_HOST_CONFIG",
				"/etc/kisage/host.json",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err.Error(), want)
				}
			}
		})
	}
}
