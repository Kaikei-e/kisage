package mcp

import (
	"fmt"

	"github.com/Kaikei-e/kisage/cli"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/plan"
)

// ToolArguments holds the typed input arguments for an MCP tool invocation (ADR 0005 §1).
// Any self-declared `Actor` field is ignored in favor of the host-bound Actor (Counterexample #16).
type ToolArguments struct {
	File             string  `json:"file"`
	Path             string  `json:"path,omitempty"`
	Expect           string  `json:"expect,omitempty"`
	OwnerRevision    *uint64 `json:"ownerRevision,omitempty"`
	DetailedExitCode bool    `json:"detailedExitCode,omitempty"`
	UntrustedActor   string  `json:"actor,omitempty"`
}

// CallToolRequest represents a direct or JSON-RPC `tools/call` invocation.
type CallToolRequest struct {
	Name      string        `json:"name"`
	Arguments ToolArguments `json:"arguments"`
}

// TextContent represents a text content block in an MCP CallToolResult.
type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// CallToolResult represents the MCP `tools/call` response containing the normative JSON document,
// exit code, and `isError` flag (ADR 0005 §1–2).
type CallToolResult struct {
	Content           []TextContent         `json:"content"`
	StructuredContent any                   `json:"structuredContent,omitempty"`
	ExitCode          int                   `json:"exitCode"`
	IsError           bool                  `json:"isError"`
	Outcome           *cli.ExecutionOutcome `json:"-"`
}

// Server exposes the 6 kisage operations over the Model Context Protocol (ADR 0005 §1–2).
type Server struct {
	host *cli.HostEnvironment
}

// NewServer constructs an MCP Server bound to the given HostEnvironment.
func NewServer(host *cli.HostEnvironment) (*Server, error) {
	if host == nil {
		return nil, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "MCP server requires a non-nil HostEnvironment",
		}
	}
	return &Server{host: host}, nil
}

// ListTools returns the 6 normative MCP tool definitions.
func (s *Server) ListTools() []ToolDefinition {
	return Tools()
}

// CallTool validates and executes an MCP tool call against the host-bound environment (ADR 0005 §1–2, Counterexample #16).
func (s *Server) CallTool(req CallToolRequest) CallToolResult {
	if s == nil || s.host == nil {
		return buildErrorResult(cli.ExitCodeUsage, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "MCP server is not initialized with a HostEnvironment",
		})
	}
	cmd, err := BuildParsedCommand(req)
	if err != nil {
		return resultFromOutcome(cli.BuildErrorOutcome(cmd, cli.ResolvedContext{}, nil, err))
	}

	outcome, _ := s.host.Execute(cmd)
	return resultFromOutcome(outcome)
}

func resultFromOutcome(outcome *cli.ExecutionOutcome) CallToolResult {
	payload := outcome.Document()
	jsonBytes, err := cli.FormatJSON(payload)
	if err != nil {
		return buildErrorResult(cli.ExitCodeError, err)
	}
	isErr := outcome.ExitCode != cli.ExitCodeOK
	if outcome.Subcommand == cli.SubcommandPlan && outcome.ExitCode == cli.ExitCodePlanDiff {
		isErr = false
	}
	return CallToolResult{
		Content: []TextContent{{
			Type: "text",
			Text: string(jsonBytes),
		}},
		StructuredContent: payload,
		ExitCode:          outcome.ExitCode,
		IsError:           isErr,
		Outcome:           outcome,
	}
}

// BuildParsedCommand purely converts an MCP CallToolRequest into a validated cli.ParsedCommand.
func BuildParsedCommand(req CallToolRequest) (cli.ParsedCommand, error) {
	sub, err := mapToolToSubcommand(req.Name)
	if err != nil {
		return cli.ParsedCommand{}, err
	}
	if req.Arguments.File == "" {
		return cli.ParsedCommand{Subcommand: sub, DetailedExitCode: req.Arguments.DetailedExitCode}, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("MCP tool %q requires 'file' argument", req.Name),
		}
	}
	cmd := cli.ParsedCommand{
		Subcommand:          sub,
		DeclarationFile:     req.Arguments.File,
		JSON:                true,
		DetailedExitCode:    req.Arguments.DetailedExitCode,
		UntrustedActorClaim: req.Arguments.UntrustedActor,
	}
	if cmd.DetailedExitCode && sub != cli.SubcommandPlan {
		return cmd, model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: "detailedExitCode is only supported for kisage_plan",
		}
	}

	switch sub {
	case cli.SubcommandPlan, cli.SubcommandApply, cli.SubcommandPrune:
		if req.Arguments.Path != "" || req.Arguments.Expect != "" || req.Arguments.OwnerRevision != nil {
			return cmd, model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Reason: fmt.Sprintf("MCP tool %q does not accept path, expect, or ownerRevision arguments", req.Name),
			}
		}
	case cli.SubcommandExplain:
		if req.Arguments.Path == "" {
			return cmd, model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Reason: "MCP tool \"kisage_explain\" requires 'path' argument",
			}
		}
		if req.Arguments.Expect != "" || req.Arguments.OwnerRevision != nil {
			return cmd, model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Reason: "MCP tool \"kisage_explain\" does not accept expect or ownerRevision arguments",
			}
		}
		p, pathErr := model.ParsePath(req.Arguments.Path)
		if pathErr != nil {
			return cmd, pathErr
		}
		cmd.TargetPath = p
	case cli.SubcommandAdopt, cli.SubcommandRelease:
		if req.Arguments.Path == "" || req.Arguments.Expect == "" || req.Arguments.OwnerRevision == nil {
			return cmd, model.Diagnostic{
				Code:   model.CodeInvalidInput,
				Reason: fmt.Sprintf("MCP tool %q requires 'file', 'path', 'expect', and 'ownerRevision' arguments", req.Name),
			}
		}
		p, pathErr := model.ParsePath(req.Arguments.Path)
		if pathErr != nil {
			return cmd, pathErr
		}
		cmd.TargetPath = p
		cmd.ExpectToken = plan.ConditionToken(req.Arguments.Expect)
		cmd.OwnerRevision = *req.Arguments.OwnerRevision
		cmd.HasOwnerRevision = true
	}
	return cmd, nil
}

func mapToolToSubcommand(toolName string) (cli.Subcommand, error) {
	switch toolName {
	case ToolPlan:
		return cli.SubcommandPlan, nil
	case ToolExplain:
		return cli.SubcommandExplain, nil
	case ToolApply:
		return cli.SubcommandApply, nil
	case ToolPrune:
		return cli.SubcommandPrune, nil
	case ToolAdopt:
		return cli.SubcommandAdopt, nil
	case ToolRelease:
		return cli.SubcommandRelease, nil
	default:
		return "", model.Diagnostic{
			Code:   model.CodeInvalidInput,
			Reason: fmt.Sprintf("unknown MCP tool %q", toolName),
		}
	}
}

func buildErrorResult(exitCode int, err error) CallToolResult {
	msg := "unknown error"
	if err != nil {
		msg = err.Error()
	}
	return CallToolResult{
		Content: []TextContent{{
			Type: "text",
			Text: msg,
		}},
		ExitCode: exitCode,
		IsError:  true,
	}
}
