package observation

import (
	"fmt"

	lkal "github.com/Kaikei-e/LKAL"
)

const defaultUnknownReason = "observation unavailable"

// Presence represents the 3-valued existence of a path in an observation (ADR 0001 §3).
// The zero value is PresenceUnknown so that uninitialized observations fail closed
// and are never implicitly treated as Absent.
type Presence uint8

const (
	// PresenceUnknown indicates that the existence of the path could not be observed
	// (e.g., due to permission errors, timeouts, partial observation failure, or missing snapshot entry).
	PresenceUnknown Presence = iota
	// PresenceAbsent indicates that the path was confirmed not to exist in the target resource.
	PresenceAbsent
	// PresencePresent indicates that a node was confirmed to exist at the path.
	PresencePresent
)

// String returns the canonical name of the Presence state.
func (p Presence) String() string {
	switch p {
	case PresenceAbsent:
		return "Absent"
	case PresencePresent:
		return "Present"
	default:
		return "Unknown"
	}
}

// Observed represents the observation sum type at a path (ADR 0001 §3):
//
//	Observed = Unknown(reason) | Absent | Present(Node)
//
// The zero value of Observed is Unknown, ensuring that uninitialized or missing
// observations fail closed rather than defaulting to Absent.
type Observed struct {
	presence Presence
	node     Node
	reason   string
}

// UnknownObserved constructs an Unknown(reason) observation.
func UnknownObserved(reason string) Observed {
	if reason == "" {
		reason = defaultUnknownReason
	}
	return Observed{
		presence: PresenceUnknown,
		reason:   reason,
	}
}

// AbsentObserved constructs an Absent observation.
func AbsentObserved() Observed {
	return Observed{presence: PresenceAbsent}
}

// PresentObserved constructs a Present(node) observation.
// If n is not a valid Node, it fails closed and returns UnknownObserved.
func PresentObserved(n Node) Observed {
	if !n.IsValid() {
		return UnknownObserved("invalid node kind in Present observation")
	}
	return Observed{
		presence: PresencePresent,
		node:     n,
	}
}

// PresentLeaf constructs a Present(Leaf(v)) observation after validating v as an LKAL leaf value.
func PresentLeaf(v lkal.Value) (Observed, error) {
	n, err := LeafNode(v)
	if err != nil {
		return Observed{}, err
	}
	return PresentObserved(n), nil
}

// MustPresentLeaf constructs a Present(Leaf(v)) observation and panics if v is invalid.
func MustPresentLeaf(v lkal.Value) Observed {
	obs, err := PresentLeaf(v)
	if err != nil {
		panic(err)
	}
	return obs
}

// PresentRecord constructs a Present(Record) observation.
func PresentRecord() Observed {
	return PresentObserved(RecordNode())
}

// PresentOpaque constructs a Present(Opaque(reason)) observation.
func PresentOpaque(reason string) Observed {
	return PresentObserved(OpaqueNode(reason))
}

// Presence returns the 3-valued existence state (Unknown, Absent, or Present).
func (o Observed) Presence() Presence {
	return o.presence
}

// IsUnknown reports whether the observation is Unknown.
func (o Observed) IsUnknown() bool {
	return o.presence == PresenceUnknown
}

// IsAbsent reports whether the path was confirmed Absent.
func (o Observed) IsAbsent() bool {
	return o.presence == PresenceAbsent
}

// IsPresent reports whether a node was confirmed Present at the path.
func (o Observed) IsPresent() bool {
	return o.presence == PresencePresent
}

// UnknownReason returns the reason why the observation is Unknown, or "" if not Unknown.
func (o Observed) UnknownReason() string {
	if o.presence != PresenceUnknown {
		return ""
	}
	if o.reason == "" {
		return defaultUnknownReason
	}
	return o.reason
}

// Node returns the underlying Node when o is Present.
func (o Observed) Node() (Node, bool) {
	if o.presence != PresencePresent {
		return Node{}, false
	}
	return o.node, true
}

// IsLeaf reports whether o is Present(Leaf(_)).
func (o Observed) IsLeaf() bool {
	return o.presence == PresencePresent && o.node.IsLeaf()
}

// LeafValue returns the leaf value if o is Present(Leaf(v)).
func (o Observed) LeafValue() (lkal.Value, bool) {
	if !o.IsLeaf() {
		return nil, false
	}
	return o.node.LeafValue()
}

// IsRecord reports whether o is Present(Record).
func (o Observed) IsRecord() bool {
	return o.presence == PresencePresent && o.node.IsRecord()
}

// IsOpaque reports whether o is Present(Opaque(_)).
func (o Observed) IsOpaque() bool {
	return o.presence == PresencePresent && o.node.IsOpaque()
}

// OpaqueReason returns the opaque reason if o is Present(Opaque(reason)).
func (o Observed) OpaqueReason() (string, bool) {
	if !o.IsOpaque() {
		return "", false
	}
	return o.node.OpaqueReason()
}

// Equal reports whether o and other represent the same observation.
func (o Observed) Equal(other Observed) bool {
	if o.presence != other.presence {
		return false
	}
	switch o.presence {
	case PresenceUnknown:
		return o.UnknownReason() == other.UnknownReason()
	case PresenceAbsent:
		return true
	case PresencePresent:
		return o.node.Equal(other.node)
	default:
		return false
	}
}

// String returns a human-readable representation of o.
func (o Observed) String() string {
	switch o.presence {
	case PresenceAbsent:
		return "Absent"
	case PresencePresent:
		return fmt.Sprintf("Present(%s)", o.node.String())
	default:
		return fmt.Sprintf("Unknown(%q)", o.UnknownReason())
	}
}

func normalizeObserved(obs Observed) Observed {
	if obs.presence == PresenceUnknown && obs.reason == "" {
		return UnknownObserved(defaultUnknownReason)
	}
	if obs.presence == PresencePresent && !obs.node.IsValid() {
		return UnknownObserved("invalid node kind in Present observation")
	}
	return obs
}

