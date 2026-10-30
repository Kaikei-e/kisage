package mcp

import (
	"fmt"

	"github.com/Kaikei-e/kisage/model"
)

// Normative MCP tool names (ADR 0005 §1).
const (
	ToolPlan    = "kisage_plan"
	ToolExplain = "kisage_explain"
	ToolApply   = "kisage_apply"
	ToolPrune   = "kisage_prune"
	ToolAdopt   = "kisage_adopt"
	ToolRelease = "kisage_release"
)

// ToolAnnotations represents the Model Context Protocol ToolAnnotations object (2025-11-25 schema, ADR 0005 §1).
//
// In accordance with ADR 0005 §1:
//   - ToolAnnotations are client hints only and NEVER act as an authorization or security enforcement mechanism.
//   - `kisage_apply` can overwrite existing leaf values and is therefore annotated with DestructiveHint = true
//     (never indiscriminately marked non-destructive).
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	DestructiveHint bool   `json:"destructiveHint"`
	IdempotentHint  bool   `json:"idempotentHint"`
	OpenWorldHint   bool   `json:"openWorldHint"`
}

// JSONSchemaProperty describes a single property in an MCP tool's inputSchema.
type JSONSchemaProperty struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

// ToolInputSchema describes the JSON Schema for an MCP tool's arguments.
type ToolInputSchema struct {
	Type                 string                        `json:"type"`
	Properties           map[string]JSONSchemaProperty `json:"properties"`
	Required             []string                      `json:"required"`
	AdditionalProperties bool                          `json:"additionalProperties"`
}

// ToolDefinition describes a single MCP tool exposed by kisage (ADR 0005 §1).
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema ToolInputSchema `json:"inputSchema"`
	Annotations ToolAnnotations `json:"annotations"`
}

// Tools returns the 6 normative MCP tool definitions with their schemas and ToolAnnotations (ADR 0005 §1).
func Tools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        ToolPlan,
			Description: "Read-only preview computing virtual crash recovery and independent Apply and Prune plans without Stabilize or persistence.",
			InputSchema: fileOnlySchema(true),
			Annotations: ToolAnnotations{
				Title:           "kisage plan (read-only preview)",
				ReadOnlyHint:    true,
				DestructiveHint: false,
				IdempotentHint:  true,
				OpenWorldHint:   false,
			},
		},
		{
			Name:        ToolExplain,
			Description: "Read-only inspection of observation, ownership, Scope permissions, Apply/Prune stopping reasons, condition token TOKEN (.explanation.conditionToken), and owner revision REV (.explanation.ownerRevision).",
			InputSchema: fileAndPathSchema(),
			Annotations: ToolAnnotations{
				Title:           "kisage explain (read-only path inspection)",
				ReadOnlyHint:    true,
				DestructiveHint: false,
				IdempotentHint:  true,
				OpenWorldHint:   false,
			},
		},
		{
			Name:        ToolApply,
			Description: "Execute crash recovery, leaf creation/update, authorized metadata updates, and in-lock re-observation. Never deletes environment leaves.",
			InputSchema: fileOnlySchema(false),
			Annotations: ToolAnnotations{
				Title:           "kisage apply (create/update leaves and metadata)",
				ReadOnlyHint:    false,
				DestructiveHint: true, // ADR 0005 §1: updating existing values can be destructive; never mark indiscriminately non-destructive.
				IdempotentHint:  true,
				OpenWorldHint:   false,
			},
		},
		{
			Name:        ToolPrune,
			Description: "Execute crash recovery, conditional deletion of retired Created leaves, and in-lock re-observation. Never performs Set or unowned adoption.",
			InputSchema: fileOnlySchema(false),
			Annotations: ToolAnnotations{
				Title:           "kisage prune (delete retired Created leaves)",
				ReadOnlyHint:    false,
				DestructiveHint: true,
				IdempotentHint:  true,
				OpenWorldHint:   false,
			},
		},
		{
			Name:        ToolAdopt,
			Description: "Explicitly adopt current representable leaf as Origin=Modified after verifying TOKEN, REV, and Stabilize. Never modifies environment values.",
			InputSchema: resolutionSchema(),
			Annotations: ToolAnnotations{
				Title:           "kisage adopt (explicit metadata adoption)",
				ReadOnlyHint:    false,
				DestructiveHint: true,
				IdempotentHint:  false,
				OpenWorldHint:   false,
			},
		},
		{
			Name:        ToolRelease,
			Description: "Explicitly release self-owned settled record and Created deletion right after verifying TOKEN, REV, and Stabilize. Never deletes environment values.",
			InputSchema: resolutionSchema(),
			Annotations: ToolAnnotations{
				Title:           "kisage release (explicit ownership release)",
				ReadOnlyHint:    false,
				DestructiveHint: true,
				IdempotentHint:  false,
				OpenWorldHint:   false,
			},
		},
	}
}

// ValidateToolDefinitions purely verifies that the MCP tool catalog satisfies ADR 0005 §1 invariants:
//   - Exactly the 6 normative tools are exposed.
//   - Only `kisage_plan` and `kisage_explain` have ReadOnlyHint == true and DestructiveHint == false.
//   - `kisage_apply`, `kisage_prune`, `kisage_adopt`, and `kisage_release` have ReadOnlyHint == false and DestructiveHint == true.
func ValidateToolDefinitions(defs []ToolDefinition) error {
	expected := map[string]bool{
		ToolPlan:    true,
		ToolExplain: true,
		ToolApply:   false,
		ToolPrune:   false,
		ToolAdopt:   false,
		ToolRelease: false,
	}
	if len(defs) != len(expected) {
		return model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("expected %d MCP tools, got %d", len(expected), len(defs)),
		}
	}
	seen := make(map[string]bool, len(defs))
	for _, d := range defs {
		wantReadOnly, ok := expected[d.Name]
		if !ok || seen[d.Name] {
			return model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Reason: fmt.Sprintf("unexpected or duplicate MCP tool %q", d.Name),
			}
		}
		seen[d.Name] = true
		if d.Annotations.ReadOnlyHint != wantReadOnly {
			return model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Reason: fmt.Sprintf("MCP tool %q ReadOnlyHint=%t, want %t", d.Name, d.Annotations.ReadOnlyHint, wantReadOnly),
			}
		}
		wantDestructive := !wantReadOnly
		if d.Annotations.DestructiveHint != wantDestructive {
			return model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Reason: fmt.Sprintf("MCP tool %q DestructiveHint=%t, want %t (ADR 0005 §1 forbids marking mutating tools non-destructive)", d.Name, d.Annotations.DestructiveHint, wantDestructive),
			}
		}
	}
	return nil
}

func fileOnlySchema(allowDetailed bool) ToolInputSchema {
	props := map[string]JSONSchemaProperty{
		"file": {
			Type:        "string",
			Description: "Path to the LKAL declaration FILE.",
		},
	}
	if allowDetailed {
		props["detailedExitCode"] = JSONSchemaProperty{
			Type:        "boolean",
			Description: "When true, compute Terraform-compatible 0/1/2 detailed exit code.",
		}
	}
	return ToolInputSchema{
		Type:                 "object",
		Properties:           props,
		Required:             []string{"file"},
		AdditionalProperties: false,
	}
}

func fileAndPathSchema() ToolInputSchema {
	return ToolInputSchema{
		Type: "object",
		Properties: map[string]JSONSchemaProperty{
			"file": {
				Type:        "string",
				Description: "Path to the LKAL declaration FILE.",
			},
			"path": {
				Type:        "string",
				Description: "Canonical leaf path (e.g., /app/port) to inspect.",
			},
		},
		Required:             []string{"file", "path"},
		AdditionalProperties: false,
	}
}

func resolutionSchema() ToolInputSchema {
	return ToolInputSchema{
		Type: "object",
		Properties: map[string]JSONSchemaProperty{
			"file": {
				Type:        "string",
				Description: "Path to the LKAL declaration FILE.",
			},
			"path": {
				Type:        "string",
				Description: "Canonical leaf path to adopt or release.",
			},
			"expect": {
				Type:        "string",
				Description: "Condition token TOKEN obtained from kisage_explain response at .explanation.conditionToken.",
			},
			"ownerRevision": {
				Type:        "integer",
				Description: "Expected ownership entry revision REV obtained from kisage_explain response at .explanation.ownerRevision.",
			},
		},
		Required:             []string{"file", "path", "expect", "ownerRevision"},
		AdditionalProperties: false,
	}
}
