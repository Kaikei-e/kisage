package observation

import (
	"fmt"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
)

// NodeKind discriminates the structural kind of a Present node (ADR 0001 §3).
type NodeKind uint8

const (
	// NodeInvalid is the zero value for an uninitialized Node.
	NodeInvalid NodeKind = iota
	// NodeLeaf represents a leaf node holding a validated lkal.Value (scalar or List).
	NodeLeaf
	// NodeRecord represents a structural container (intermediate or empty Record).
	NodeRecord
	// NodeOpaque represents a node that exists in the target resource but cannot be
	// safely represented as an LKAL Leaf or Record.
	NodeOpaque
)

// String returns the canonical name of the NodeKind.
func (k NodeKind) String() string {
	switch k {
	case NodeLeaf:
		return "Leaf"
	case NodeRecord:
		return "Record"
	case NodeOpaque:
		return "Opaque"
	default:
		return "Invalid"
	}
}

// Node represents the structural kind and payload of an existing node at a path:
//
//	Node = Leaf(lkal.Value) | Record | Opaque(reason)
type Node struct {
	kind          NodeKind
	leaf          lkal.Value
	reason        string
	opaqueWitness string
}

// LeafNode constructs a Leaf node after validating and normalizing v as an LKAL leaf value.
func LeafNode(v lkal.Value) (Node, error) {
	norm, err := model.ValidateLeafValue(v)
	if err != nil {
		return Node{}, err
	}
	return Node{
		kind: NodeLeaf,
		leaf: norm,
	}, nil
}

// MustLeafNode constructs a Leaf node from v and panics if v is not a valid LKAL leaf value.
func MustLeafNode(v lkal.Value) Node {
	n, err := LeafNode(v)
	if err != nil {
		panic(err)
	}
	return n
}

// RecordNode constructs a Record node.
func RecordNode() Node {
	return Node{kind: NodeRecord}
}

// OpaqueNode constructs an Opaque node with the given reason.
func OpaqueNode(reason string) Node {
	if reason == "" {
		reason = "unrepresentable target value"
	}
	return Node{
		kind:   NodeOpaque,
		reason: reason,
	}
}

// OpaqueNodeWithWitness constructs an Opaque node with both a diagnostic reason and a raw-value witness.
func OpaqueNodeWithWitness(reason, witness string) Node {
	n := OpaqueNode(reason)
	n.opaqueWitness = witness
	return n
}

// Kind returns the NodeKind of n.
func (n Node) Kind() NodeKind {
	return n.kind
}

// IsValid reports whether n is a properly initialized Leaf, Record, or Opaque node.
func (n Node) IsValid() bool {
	return n.kind == NodeLeaf || n.kind == NodeRecord || n.kind == NodeOpaque
}

// IsLeaf reports whether n is a Leaf node.
func (n Node) IsLeaf() bool {
	return n.kind == NodeLeaf
}

// IsRecord reports whether n is a Record node.
func (n Node) IsRecord() bool {
	return n.kind == NodeRecord
}

// IsOpaque reports whether n is an Opaque node.
func (n Node) IsOpaque() bool {
	return n.kind == NodeOpaque
}

// LeafValue returns the leaf value if n is a Leaf node.
func (n Node) LeafValue() (lkal.Value, bool) {
	if n.kind != NodeLeaf {
		return nil, false
	}
	return model.CloneLeafValue(n.leaf), true
}

// OpaqueReason returns the explanatory reason if n is an Opaque node.
func (n Node) OpaqueReason() (string, bool) {
	if n.kind != NodeOpaque {
		return "", false
	}
	return n.reason, true
}

// OpaqueWitness returns the optional raw-value witness if n is an Opaque node.
func (n Node) OpaqueWitness() string {
	if n.kind != NodeOpaque {
		return ""
	}
	return n.opaqueWitness
}

// Equal reports whether n and other have the same kind and payload.
func (n Node) Equal(other Node) bool {
	if n.kind != other.kind {
		return false
	}
	switch n.kind {
	case NodeLeaf:
		return model.EqualLeafValues(n.leaf, other.leaf)
	case NodeRecord:
		return true
	case NodeOpaque:
		if n.reason != other.reason {
			return false
		}
		if n.opaqueWitness != "" && other.opaqueWitness != "" {
			return n.opaqueWitness == other.opaqueWitness
		}
		return true
	default:
		return true
	}
}

// String returns a human-readable representation of n.
func (n Node) String() string {
	switch n.kind {
	case NodeLeaf:
		return fmt.Sprintf("Leaf(%v)", n.leaf)
	case NodeRecord:
		return "Record"
	case NodeOpaque:
		return fmt.Sprintf("Opaque(%q)", n.reason)
	default:
		return "InvalidNode"
	}
}
