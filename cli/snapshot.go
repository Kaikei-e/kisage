package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/projection"
	"github.com/Kaikei-e/kisage/provider"
)

const digestPrefix = "sha256:"

// DeclarationSnapshot holds the immutable start-of-call snapshot of a declaration FILE,
// its canonical SHA-256 digest, its physical file identity (when loaded from disk), and its
// projected leaf paths D = Paths(d) (ADR 0005 §1–2, Counterexample #14).
type DeclarationSnapshot struct {
	FilePath    string
	Digest      string
	Physical    ownership.PhysicalIdentity
	HasPhysical bool
	Declaration *projection.Declaration
}

// ComputeDeclarationDigest purely computes the canonical SHA-256 digest string ("sha256:<hex>")
// for raw declaration bytes (ADR 0005 §1–2, Counterexample #14).
func ComputeDeclarationDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return digestPrefix + hex.EncodeToString(sum[:])
}

// BuildDeclarationSnapshot purely constructs a DeclarationSnapshot from in-memory bytes.
// Even if LKAL parsing or projection fails (e.g., K101 syntax error or K102 non-root empty Record),
// the returned *DeclarationSnapshot contains the deterministic Digest of raw so callers can report it.
func BuildDeclarationSnapshot(filePath string, raw []byte) (*DeclarationSnapshot, error) {
	if filePath == "" {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "declaration file path must not be empty",
		}
	}
	digest := ComputeDeclarationDigest(raw)
	decl, err := projection.ParseDeclaration(filePath, raw)
	snap := &DeclarationSnapshot{
		FilePath:    filePath,
		Digest:      digest,
		Declaration: decl,
	}
	if err != nil {
		return snap, err
	}
	return snap, nil
}

// ReadDeclarationSnapshot reads the declaration FILE once at the start of a command invocation
// without following symlinks or blocking on special files, computes its SHA-256 digest, and projects
// its leaf paths (ADR 0005 §1, Counterexamples #14, #17).
func ReadDeclarationSnapshot(filePath string) (*DeclarationSnapshot, error) {
	clean, err := validateDeclarationFilePath(filePath)
	if err != nil {
		return nil, err
	}
	raw, phys, err := readRegularDeclarationFile(clean)
	if err != nil {
		return nil, err
	}
	snap, parseErr := BuildDeclarationSnapshot(clean, raw)
	if snap != nil {
		snap.Physical = phys
		snap.HasPhysical = true
	}
	return snap, parseErr
}

func validateDeclarationFilePath(filePath string) (string, error) {
	if filePath == "" {
		return "", model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "declaration FILE is required",
		}
	}
	clean := filepath.Clean(filePath)
	if !filepath.IsAbs(clean) {
		abs, err := filepath.Abs(clean)
		if err != nil {
			return "", model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Reason: fmt.Sprintf("failed to resolve declaration path %q: %v", filePath, err),
				Cause:  err,
			}
		}
		clean = abs
	}
	return clean, nil
}

func readRegularDeclarationFile(cleanPath string) ([]byte, ownership.PhysicalIdentity, error) {
	// Protect every directory component, not just the final file, using the
	// same no-follow, non-blocking handle inspection as the local provider.
	meta, data, err := provider.InspectLocalFile(filepath.Dir(cleanPath), cleanPath)
	if err != nil {
		return nil, ownership.PhysicalIdentity{}, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("failed to read declaration file %q safely: %v", cleanPath, err),
			Cause:  err,
		}
	}
	return data, meta.Physical, nil
}
