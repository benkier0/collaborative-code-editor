package ot

import (
	"encoding/json"
	"fmt"
)

// OpType is the kind of operation component.
type OpType string

const (
	Retain OpType = "retain"
	Insert OpType = "insert"
	Delete OpType = "delete"
)

// Component is a single part of a compound operation.
type Component struct {
	Type    OpType `json:"type"`
	N       int    `json:"n,omitempty"`       // for Retain and Delete
	Content string `json:"content,omitempty"` // for Insert
}

// UnmarshalJSON handles both {type:"retain",n:5} and {type:"insert",content:"x"}
func (c *Component) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type    OpType `json:"type"`
		N       int    `json:"n"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.Type = raw.Type
	c.N = raw.N
	c.Content = raw.Content
	return nil
}

// Op is a sequence of components representing one user's edit.
type Op struct {
	Components []Component `json:"components"`
	BaseRev    int         `json:"baseRev,omitempty"`
}

func (o *Op) InputLen() int {
	n := 0
	for _, c := range o.Components {
		switch c.Type {
		case Retain:
			n += c.N
		case Delete:
			n += c.N
		}
	}
	return n
}

func (o *Op) OutputLen() int {
	n := 0
	for _, c := range o.Components {
		switch c.Type {
		case Retain:
			n += c.N
		case Insert:
			n += len([]rune(c.Content))
		}
	}
	return n
}

// Apply applies op to doc and returns the new document.
func Apply(doc string, op Op) (string, error) {
	result := []byte{}
	pos := 0
	runes := []rune(doc)

	for _, c := range op.Components {
		switch c.Type {
		case Retain:
			if pos+c.N > len(runes) {
				return "", fmt.Errorf("retain past end of doc: pos=%d n=%d doclen=%d", pos, c.N, len(runes))
			}
			result = append(result, []byte(string(runes[pos:pos+c.N]))...)
			pos += c.N
		case Insert:
			result = append(result, []byte(c.Content)...)
		case Delete:
			if pos+c.N > len(runes) {
				return "", fmt.Errorf("delete past end of doc: pos=%d n=%d doclen=%d", pos, c.N, len(runes))
			}
			pos += c.N
		}
	}
	if pos != len(runes) {
		return "", fmt.Errorf("op did not consume full doc: consumed=%d doclen=%d", pos, len(runes))
	}
	return string(result), nil
}

// Transform takes two concurrent ops a and b built against the same revision
// and returns (a', b') such that apply(apply(doc,a),b') == apply(apply(doc,b),a')
func Transform(a, b Op) (Op, Op, error) {
	aPrime := Op{}
	bPrime := Op{}

	ai := newIter(a.Components)
	bi := newIter(b.Components)

	for !ai.done() || !bi.done() {
		ac, aok := ai.peek()
		bc, bok := bi.peek()

		if aok && ac.Type == Insert {
			aPrime.append(ac)
			bPrime.append(Component{Type: Retain, N: len([]rune(ac.Content))})
			ai.consume()
			continue
		}
		if bok && bc.Type == Insert {
			bPrime.append(bc)
			aPrime.append(Component{Type: Retain, N: len([]rune(bc.Content))})
			bi.consume()
			continue
		}

		if !aok || !bok {
			return Op{}, Op{}, fmt.Errorf("ops have different input lengths")
		}

		switch {
		case ac.Type == Retain && bc.Type == Retain:
			n := min2(ac.N, bc.N)
			aPrime.append(Component{Type: Retain, N: n})
			bPrime.append(Component{Type: Retain, N: n})
			ai.advance(n)
			bi.advance(n)

		case ac.Type == Delete && bc.Type == Delete:
			n := min2(ac.N, bc.N)
			ai.advance(n)
			bi.advance(n)

		case ac.Type == Delete && bc.Type == Retain:
			n := min2(ac.N, bc.N)
			aPrime.append(Component{Type: Delete, N: n})
			ai.advance(n)
			bi.advance(n)

		case ac.Type == Retain && bc.Type == Delete:
			n := min2(ac.N, bc.N)
			bPrime.append(Component{Type: Delete, N: n})
			ai.advance(n)
			bi.advance(n)
		}
	}

	return aPrime, bPrime, nil
}

// Compose produces a single op equivalent to applying a then b.
func Compose(a, b Op) (Op, error) {
	result := Op{}
	ai := newIter(a.Components)
	bi := newIter(b.Components)

	for !ai.done() || !bi.done() {
		bc, bok := bi.peek()

		if bok && bc.Type == Insert {
			result.append(bc)
			bi.consume()
			continue
		}

		ac, aok := ai.peek()
		if aok && ac.Type == Delete {
			result.append(ac)
			ai.consume()
			continue
		}

		if !aok || !bok {
			return Op{}, fmt.Errorf("compose: mismatched lengths")
		}

		switch {
		case ac.Type == Retain && bc.Type == Retain:
			n := min2(ac.N, bc.N)
			result.append(Component{Type: Retain, N: n})
			ai.advance(n)
			bi.advance(n)

		case ac.Type == Retain && bc.Type == Delete:
			n := min2(ac.N, bc.N)
			result.append(Component{Type: Delete, N: n})
			ai.advance(n)
			bi.advance(n)

		case ac.Type == Insert && bc.Type == Retain:
			n := min2(len([]rune(ac.Content)), bc.N)
			result.append(Component{Type: Insert, Content: string([]rune(ac.Content)[:n])})
			ai.advanceInsert(n)
			bi.advance(n)

		case ac.Type == Insert && bc.Type == Delete:
			n := min2(len([]rune(ac.Content)), bc.N)
			ai.advanceInsert(n)
			bi.advance(n)
		}
	}

	return result, nil
}

func (o *Op) append(c Component) {
	if len(o.Components) == 0 {
		o.Components = append(o.Components, c)
		return
	}
	last := &o.Components[len(o.Components)-1]
	switch {
	case last.Type == Retain && c.Type == Retain:
		last.N += c.N
	case last.Type == Delete && c.Type == Delete:
		last.N += c.N
	case last.Type == Insert && c.Type == Insert:
		last.Content += c.Content
	default:
		o.Components = append(o.Components, c)
	}
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type iter struct {
	comps  []Component
	idx    int
	offset int
}

func newIter(comps []Component) *iter { return &iter{comps: comps} }
func (it *iter) done() bool           { return it.idx >= len(it.comps) }

func (it *iter) peek() (Component, bool) {
	if it.done() {
		return Component{}, false
	}
	c := it.comps[it.idx]
	switch c.Type {
	case Retain:
		return Component{Type: Retain, N: c.N - it.offset}, true
	case Delete:
		return Component{Type: Delete, N: c.N - it.offset}, true
	case Insert:
		return Component{Type: Insert, Content: string([]rune(c.Content)[it.offset:])}, true
	}
	return Component{}, false
}

func (it *iter) consume() {
	it.idx++
	it.offset = 0
}

func (it *iter) advance(n int) {
	remaining := n
	for remaining > 0 && !it.done() {
		c := it.comps[it.idx]
		var size int
		if c.Type == Insert {
			size = len([]rune(c.Content)) - it.offset
		} else {
			size = c.N - it.offset
		}
		if size <= remaining {
			remaining -= size
			it.idx++
			it.offset = 0
		} else {
			it.offset += remaining
			remaining = 0
		}
	}
}

func (it *iter) advanceInsert(n int) { it.advance(n) }