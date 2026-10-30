package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Kaikei-e/kisage/host"
	"github.com/Kaikei-e/kisage/provider"
)

//go:embed desired.lkal config.lkal
var templates embed.FS

// initialize provisions a fresh, private host sandbox. Existing directories are
// rejected so rerunning setup cannot erase configuration or ownership history.
func initialize(directory string, stdout io.Writer) error {
	root, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		return fmt.Errorf("create a NEW sandbox directory: %w", err)
	}
	for _, dir := range []string{"state", "locks"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			return err
		}
	}
	for _, name := range []string{"desired.lkal", "config.lkal"} {
		raw, err := templates.ReadFile(name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, name), raw, 0o600); err != nil {
			return err
		}
	}
	target := filepath.Join(root, "config.lkal")
	identity, err := provider.InspectPhysicalIdentity(target)
	if err != nil {
		return err
	}
	cfg := host.Config{
		Version: 1, Actor: "healthcheck", StoreID: "healthcheck-store",
		StoreDirectory: filepath.Join(root, "state"),
		LockDirectory:  filepath.Join(root, "locks"),
		Resources: []host.ResourceConfig{{
			ID: "healthcheck-config", ManagementRoot: root, TargetPath: target,
			DeviceID: identity.DeviceID, DeclarationFile: filepath.Join(root, "desired.lkal"),
			Scope: []host.RuleConfig{{
				Path: "/checks", Match: "subtree", Permissions: []string{"Set", "Own", "Delete"},
			}},
		}},
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	policy := filepath.Join(root, "host.json")
	if err := os.WriteFile(policy, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	if err := host.InitializeStore(policy); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Sandbox: %s\nHost policy: %s\nEdit desired.lkal, then run kisage plan / apply.\n", root, policy)
	return nil
}
