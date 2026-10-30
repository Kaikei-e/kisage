package ownership_test

import (
	"testing"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
)

// TestCounterexample16_HostBoundActorTrustBoundary verifies ADR 0002 §4 and Counterexample #16:
//   - Self-declared CLI `--actor` or MCP tool arguments are never trusted to override the host-bound Actor.
//   - ResolveActor always binds to the host-configured ActorID (and fails closed if hostBound is empty).
//   - VerifyActorClaim rejects mismatched untrusted claims with K401 PolicyDenied.
func TestCounterexample16_HostBoundActorTrustBoundary(t *testing.T) {
	hostActor := ownership.ActorID("host-bound-agent")

	// ResolveActor ignores any untrusted claim and returns the host-bound Actor.
	bound, err := ownership.ResolveActor(hostActor, "spoofed-admin-actor")
	if err != nil {
		t.Fatalf("ResolveActor failed: %v", err)
	}
	if bound != hostActor {
		t.Fatalf("ResolveActor returned %q, want host-bound %q", bound, hostActor)
	}

	// VerifyActorClaim rejects mismatched self-declared actor claims with K401 PolicyDenied.
	_, err = ownership.VerifyActorClaim(hostActor, "spoofed-admin-actor")
	assertDiagnosticCode(t, err, model.CodePolicyDenied)

	// Matching or empty claim succeeds with hostActor.
	for _, claim := range []string{"", string(hostActor)} {
		got, err := ownership.VerifyActorClaim(hostActor, claim)
		if err != nil || got != hostActor {
			t.Fatalf("VerifyActorClaim(%q, %q) = (%q, %v), want (%q, nil)", hostActor, claim, got, err, hostActor)
		}
	}

	// Missing host-bound Actor fails closed with K101.
	_, err = ownership.ResolveActor("", "spoofed-admin-actor")
	assertDiagnosticCode(t, err, model.CodeInvalidInput)
}

// TestCounterexample33_ResourcePhysicalIdentityAndHardlinkRejection verifies ADR 0002 §1 and Counterexample #33:
//   - Single-file provider rejects target files with LinkCount > 1 (hard links).
//   - TargetPath must reside strictly inside ManagementRoot.
//   - ResourceRegistry rejects registering the same physical file (DeviceID, Inode) or canonical path
//     under another ResourceID (path alias) or another StoreID (separate ownership store).
func TestCounterexample33_ResourcePhysicalIdentityAndHardlinkRejection(t *testing.T) {
	validBinding := ownership.ResourceBinding{
		ID:             "app-config",
		Provider:       ownership.ProviderLKALFile,
		ManagementRoot: "/etc/kisage/managed",
		TargetPath:     "/etc/kisage/managed/app.lkal",
		StoreID:        "store-main",
		Physical: ownership.PhysicalIdentity{
			DeviceID:  2049,
			Inode:     90001,
			LinkCount: 1,
		},
	}

	reg, err := ownership.NewResourceRegistry(validBinding)
	if err != nil {
		t.Fatalf("NewResourceRegistry failed: %v", err)
	}

	t.Run("rejects hardlink target (LinkCount > 1)", func(t *testing.T) {
		hardlinked := validBinding
		hardlinked.ID = "hardlinked-config"
		hardlinked.TargetPath = "/etc/kisage/managed/hardlink.lkal"
		hardlinked.Physical.Inode = 90002
		hardlinked.Physical.LinkCount = 2
		assertDiagnosticCode(t, ownership.ValidateResourceBinding(hardlinked), model.CodeInvalidInput)
	})

	t.Run("rejects same physical inode via different alias path", func(t *testing.T) {
		aliasedPath := validBinding
		aliasedPath.ID = "app-config-alias"
		aliasedPath.TargetPath = "/etc/kisage/managed/subdir/alias.lkal"
		// Same (DeviceID=2049, Inode=90001)
		_, err := reg.Register(aliasedPath)
		assertDiagnosticCode(t, err, model.CodeInvalidInput)
	})

	t.Run("rejects same physical file registered under a different ownership store", func(t *testing.T) {
		otherStore := validBinding
		otherStore.ID = "app-config-other-store"
		otherStore.StoreID = "store-secondary"
		_, err := reg.Register(otherStore)
		assertDiagnosticCode(t, err, model.CodeInvalidInput)
	})

	t.Run("rejects target path outside management root", func(t *testing.T) {
		outsideRoot := validBinding
		outsideRoot.ID = "escaped-config"
		outsideRoot.TargetPath = "/etc/other/app.lkal"
		outsideRoot.Physical.Inode = 90003
		assertDiagnosticCode(t, ownership.ValidateResourceBinding(outsideRoot), model.CodeInvalidInput)
	})
}
