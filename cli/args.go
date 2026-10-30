package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/plan"
)

// Subcommand identifies one of the 6 normative CLI subcommands in ADR 0005 §1.
type Subcommand string

const (
	SubcommandPlan    Subcommand = "plan"
	SubcommandApply   Subcommand = "apply"
	SubcommandPrune   Subcommand = "prune"
	SubcommandExplain Subcommand = "explain"
	SubcommandAdopt   Subcommand = "adopt"
	SubcommandRelease Subcommand = "release"
)

// ParsedCommand holds the validated CLI subcommand, positional arguments, and flags (ADR 0005 §1).
type ParsedCommand struct {
	Subcommand          Subcommand
	DeclarationFile     string
	TargetPath          model.Path
	ExpectToken         plan.ConditionToken
	OwnerRevision       uint64
	HasOwnerRevision    bool
	JSON                bool
	DetailedExitCode    bool
	UntrustedActorClaim string
}

// ParseArgs purely parses CLI command-line arguments (excluding `os.Args[0]`) for the 6 normative
// kisage subcommands (ADR 0005 §1):
//   - `kisage plan FILE [-json] [-detailed-exitcode]`
//   - `kisage apply FILE [-json]`
//   - `kisage prune FILE [-json]`
//   - `kisage explain FILE PATH [-json]`
//   - `kisage adopt FILE PATH --expect TOKEN --owner-revision REV [-json]`
//   - `kisage release FILE PATH --expect TOKEN --owner-revision REV [-json]`
//
// Flags may appear before, between, or after positional arguments. Any self-declared `--actor` flag
// is captured in UntrustedActorClaim and ignored in favor of the host binding (Counterexample #16).
func ParseArgs(args []string) (ParsedCommand, error) {
	if len(args) == 0 {
		return ParsedCommand{}, usageError("missing subcommand; expected one of: plan, apply, prune, explain, adopt, release")
	}
	sub := Subcommand(args[0])
	switch sub {
	case SubcommandPlan, SubcommandApply, SubcommandPrune, SubcommandExplain, SubcommandAdopt, SubcommandRelease:
	default:
		return ParsedCommand{}, usageError("unknown subcommand %q; expected one of: plan, apply, prune, explain, adopt, release", args[0])
	}

	cmd, positionals, err := parseFlagsAndPositionals(sub, args[1:])
	if err != nil {
		return cmd, err
	}
	return bindPositionalsAndValidate(cmd, positionals)
}

func parseFlagsAndPositionals(sub Subcommand, rest []string) (ParsedCommand, []string, error) {
	cmd := ParsedCommand{Subcommand: sub}
	var positionals []string

	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals = append(positionals, arg)
			continue
		}
		name, val, hasEq := splitFlag(arg)
		switch name {
		case "json":
			b, err := parseBoolFlag("json", val, hasEq)
			if err != nil {
				return cmd, nil, err
			}
			cmd.JSON = b
		case "detailed-exitcode":
			b, err := parseBoolFlag("detailed-exitcode", val, hasEq)
			if err != nil {
				return cmd, nil, err
			}
			cmd.DetailedExitCode = b
		case "expect":
			v, nextIdx, err := consumeStringFlag("expect", val, hasEq, rest, i)
			if err != nil {
				return cmd, nil, err
			}
			i = nextIdx
			cmd.ExpectToken = plan.ConditionToken(v)
		case "owner-revision":
			v, nextIdx, err := consumeStringFlag("owner-revision", val, hasEq, rest, i)
			if err != nil {
				return cmd, nil, err
			}
			i = nextIdx
			rev, parseErr := strconv.ParseUint(v, 10, 64)
			if parseErr != nil {
				return cmd, nil, usageError("invalid --owner-revision %q: must be an unsigned 64-bit integer", v)
			}
			cmd.OwnerRevision = rev
			cmd.HasOwnerRevision = true
		case "actor":
			v, nextIdx, err := consumeStringFlag("actor", val, hasEq, rest, i)
			if err != nil {
				return cmd, nil, err
			}
			i = nextIdx
			cmd.UntrustedActorClaim = v
		default:
			return cmd, nil, usageError("unknown flag %q for subcommand %s", arg, sub)
		}
	}
	return cmd, positionals, nil
}

func bindPositionalsAndValidate(cmd ParsedCommand, positionals []string) (ParsedCommand, error) {
	if cmd.DetailedExitCode && cmd.Subcommand != SubcommandPlan {
		return cmd, usageError("-detailed-exitcode is only supported for 'kisage plan'")
	}
	switch cmd.Subcommand {
	case SubcommandPlan, SubcommandApply, SubcommandPrune:
		if len(positionals) != 1 {
			return cmd, usageError("subcommand %q requires exactly 1 positional argument (FILE), got %d", cmd.Subcommand, len(positionals))
		}
		if !cmd.ExpectToken.IsEmpty() || cmd.HasOwnerRevision {
			return cmd, usageError("--expect and --owner-revision are only supported for 'adopt' and 'release'")
		}
		cmd.DeclarationFile = positionals[0]
	case SubcommandExplain:
		if len(positionals) != 2 {
			return cmd, usageError("subcommand \"explain\" requires 2 positional arguments (FILE PATH), got %d", len(positionals))
		}
		if !cmd.ExpectToken.IsEmpty() || cmd.HasOwnerRevision {
			return cmd, usageError("--expect and --owner-revision are not supported for 'explain'")
		}
		cmd.DeclarationFile = positionals[0]
		p, err := model.ParsePath(positionals[1])
		if err != nil {
			return cmd, err
		}
		if err := model.ValidateLeafPath(p); err != nil {
			return cmd, err
		}
		cmd.TargetPath = p
	case SubcommandAdopt, SubcommandRelease:
		if len(positionals) != 2 {
			return cmd, usageError("subcommand %q requires 2 positional arguments (FILE PATH), got %d", cmd.Subcommand, len(positionals))
		}
		if cmd.ExpectToken.IsEmpty() {
			return cmd, usageError("subcommand %q requires --expect TOKEN", cmd.Subcommand)
		}
		if !cmd.HasOwnerRevision {
			return cmd, usageError("subcommand %q requires --owner-revision REV", cmd.Subcommand)
		}
		cmd.DeclarationFile = positionals[0]
		p, err := model.ParsePath(positionals[1])
		if err != nil {
			return cmd, err
		}
		if err := model.ValidateLeafPath(p); err != nil {
			return cmd, err
		}
		cmd.TargetPath = p
	}
	return cmd, nil
}

func splitFlag(arg string) (string, string, bool) {
	trimmed := strings.TrimLeft(arg, "-")
	if idx := strings.IndexByte(trimmed, '='); idx >= 0 {
		return trimmed[:idx], trimmed[idx+1:], true
	}
	return trimmed, "", false
}

func parseBoolFlag(name, val string, hasEq bool) (bool, error) {
	if !hasEq {
		return true, nil
	}
	b, err := strconv.ParseBool(val)
	if err != nil {
		return false, usageError("invalid boolean value %q for -%s", val, name)
	}
	return b, nil
}

func consumeStringFlag(name, val string, hasEq bool, rest []string, idx int) (string, int, error) {
	if hasEq {
		if val == "" {
			return "", idx, usageError("flag --%s requires a non-empty value", name)
		}
		return val, idx, nil
	}
	if idx+1 >= len(rest) {
		return "", idx, usageError("flag --%s requires a value", name)
	}
	next := rest[idx+1]
	if next == "" {
		return "", idx + 1, usageError("flag --%s requires a non-empty value", name)
	}
	return next, idx + 1, nil
}

func usageError(format string, a ...any) error {
	return model.Diagnostic{
		Code:   model.CodeInvalidInput,
		Reason: fmt.Sprintf(format, a...),
	}
}
