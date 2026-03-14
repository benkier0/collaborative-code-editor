package ot

import (
	"testing"
)

// convergence is the core OT property: applying a then b' == applying b then a'
func assertConvergence(t *testing.T, doc string, a, b Op) {
	t.Helper()

	aPrime, bPrime, err := Transform(a, b)
	if err != nil {
		t.Fatalf("Transform failed: %v", err)
	}

	afterA, err := Apply(doc, a)
	if err != nil {
		t.Fatalf("Apply a failed: %v", err)
	}
	afterAB, err := Apply(afterA, bPrime)
	if err != nil {
		t.Fatalf("Apply b' failed: %v", err)
	}

	afterB, err := Apply(doc, b)
	if err != nil {
		t.Fatalf("Apply b failed: %v", err)
	}
	afterBA, err := Apply(afterB, aPrime)
	if err != nil {
		t.Fatalf("Apply a' failed: %v", err)
	}

	if afterAB != afterBA {
		t.Errorf("Convergence failure:\n  doc=%q\n  a=%v\n  b=%v\n  after a then b'=%q\n  after b then a'=%q",
			doc, a, b, afterAB, afterBA)
	}
}

func TestInsertInsert_samePosition(t *testing.T) {
	doc := "hello"
	a := Op{Components: []Component{{Type: Insert, Content: "X"}, {Type: Retain, N: 5}}}
	b := Op{Components: []Component{{Type: Insert, Content: "Y"}, {Type: Retain, N: 5}}}
	assertConvergence(t, doc, a, b)
}

func TestInsertInsert_differentPositions(t *testing.T) {
	doc := "hello world"
	// a inserts "A" at position 5
	a := Op{Components: []Component{
		{Type: Retain, N: 5},
		{Type: Insert, Content: "A"},
		{Type: Retain, N: 6},
	}}
	// b inserts "B" at position 6
	b := Op{Components: []Component{
		{Type: Retain, N: 6},
		{Type: Insert, Content: "B"},
		{Type: Retain, N: 5},
	}}
	assertConvergence(t, doc, a, b)
}

func TestDeleteDelete_overlap(t *testing.T) {
	doc := "abcdef"
	// a deletes chars 1-3 ("bcd")
	a := Op{Components: []Component{
		{Type: Retain, N: 1},
		{Type: Delete, N: 3},
		{Type: Retain, N: 2},
	}}
	// b deletes chars 2-4 ("cde")
	b := Op{Components: []Component{
		{Type: Retain, N: 2},
		{Type: Delete, N: 3},
		{Type: Retain, N: 1},
	}}
	assertConvergence(t, doc, a, b)
}

func TestInsertDelete_concurrent(t *testing.T) {
	doc := "hello world"
	// a inserts " beautiful" at position 5
	a := Op{Components: []Component{
		{Type: Retain, N: 5},
		{Type: Insert, Content: " beautiful"},
		{Type: Retain, N: 6},
	}}
	// b deletes " world" (positions 5-11)
	b := Op{Components: []Component{
		{Type: Retain, N: 5},
		{Type: Delete, N: 6},
	}}
	assertConvergence(t, doc, a, b)
}

func TestDeleteInsert_concurrent(t *testing.T) {
	doc := "abcde"
	// a deletes "b" at position 1
	a := Op{Components: []Component{
		{Type: Retain, N: 1},
		{Type: Delete, N: 1},
		{Type: Retain, N: 3},
	}}
	// b inserts "X" at position 3
	b := Op{Components: []Component{
		{Type: Retain, N: 3},
		{Type: Insert, Content: "X"},
		{Type: Retain, N: 2},
	}}
	assertConvergence(t, doc, a, b)
}

func TestApply_insertAtStart(t *testing.T) {
	doc := "world"
	op := Op{Components: []Component{
		{Type: Insert, Content: "hello "},
		{Type: Retain, N: 5},
	}}
	result, err := Apply(doc, op)
	if err != nil {
		t.Fatal(err)
	}
	if result != "hello world" {
		t.Errorf("expected %q got %q", "hello world", result)
	}
}

func TestApply_deleteMiddle(t *testing.T) {
	doc := "hello world"
	op := Op{Components: []Component{
		{Type: Retain, N: 5},
		{Type: Delete, N: 6},
	}}
	result, err := Apply(doc, op)
	if err != nil {
		t.Fatal(err)
	}
	if result != "hello" {
		t.Errorf("expected %q got %q", "hello", result)
	}
}

func TestApply_replace(t *testing.T) {
	doc := "foo bar"
	op := Op{Components: []Component{
		{Type: Retain, N: 4},
		{Type: Delete, N: 3},
		{Type: Insert, Content: "baz"},
	}}
	result, err := Apply(doc, op)
	if err != nil {
		t.Fatal(err)
	}
	if result != "foo baz" {
		t.Errorf("expected %q got %q", "foo baz", result)
	}
}

func TestCompose(t *testing.T) {
	doc := "abc"
	a := Op{Components: []Component{
		{Type: Retain, N: 1},
		{Type: Insert, Content: "X"},
		{Type: Retain, N: 2},
	}}
	b := Op{Components: []Component{
		{Type: Retain, N: 3},
		{Type: Insert, Content: "Y"},
		{Type: Retain, N: 1}, // "c"
	}}

	afterA, _ := Apply(doc, a)
	afterAB, _ := Apply(afterA, b)

	composed, err := Compose(a, b)
	if err != nil {
		t.Fatal(err)
	}
	afterComposed, err := Apply(doc, composed)
	if err != nil {
		t.Fatal(err)
	}
	if afterComposed != afterAB {
		t.Errorf("Compose: expected %q got %q", afterAB, afterComposed)
	}
}

func TestTransform_multipleOpsConverge(t *testing.T) {
	// Stress: multiple concurrent inserts at the same position
	doc := "start end"
	ops := []Op{
		{Components: []Component{{Type: Retain, N: 5}, {Type: Insert, Content: "A"}, {Type: Retain, N: 4}}},
		{Components: []Component{{Type: Retain, N: 5}, {Type: Insert, Content: "B"}, {Type: Retain, N: 4}}},
		{Components: []Component{{Type: Retain, N: 5}, {Type: Insert, Content: "C"}, {Type: Retain, N: 4}}},
	}
	// Transform all pairs — they should all converge
	for i := 0; i < len(ops); i++ {
		for j := i + 1; j < len(ops); j++ {
			assertConvergence(t, doc, ops[i], ops[j])
		}
	}
}
