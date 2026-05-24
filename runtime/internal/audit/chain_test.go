package audit

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCanonicalJSON_KeyOrderingIsDeterministic(t *testing.T) {
	// Two maps with the same logical content but written in
	// different key orders should produce identical canonical bytes.
	a := map[string]any{
		"zebra":  1,
		"alpha":  2,
		"middle": 3,
	}
	b := map[string]any{
		"middle": 3,
		"alpha":  2,
		"zebra":  1,
	}

	aCanon, err := canonicalJSON(a)
	if err != nil {
		t.Fatalf("canonicalJSON(a): %v", err)
	}
	bCanon, err := canonicalJSON(b)
	if err != nil {
		t.Fatalf("canonicalJSON(b): %v", err)
	}
	if string(aCanon) != string(bCanon) {
		t.Errorf("expected identical canonical forms:\n  a: %s\n  b: %s", aCanon, bCanon)
	}
	if !strings.Contains(string(aCanon), `"alpha":2,"middle":3,"zebra":1`) {
		t.Errorf("keys not sorted: %s", aCanon)
	}
}

func TestCanonicalJSON_RecursivelySorts(t *testing.T) {
	input := map[string]any{
		"outer_z": map[string]any{
			"inner_z": 1,
			"inner_a": 2,
		},
		"outer_a": []any{
			map[string]any{"y": 1, "x": 2},
		},
	}
	got, err := canonicalJSON(input)
	if err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}
	want := `{"outer_a":[{"x":2,"y":1}],"outer_z":{"inner_a":2,"inner_z":1}}`
	if string(got) != want {
		t.Errorf("got:\n  %s\nwant:\n  %s", got, want)
	}
}

func TestCanonicalJSON_PreservesIntegerPrecision(t *testing.T) {
	// Integer values that exceed float64 precision should survive
	// the canonicalization (UseNumber path).
	const huge = int64(9007199254740993) // 2^53 + 1 — not representable as float64
	input := map[string]any{"big": huge}
	got, err := canonicalJSON(input)
	if err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}
	if !strings.Contains(string(got), "9007199254740993") {
		t.Errorf("integer precision lost: %s", got)
	}
}

func TestCanonicalJSON_ArrayPreservesOrder(t *testing.T) {
	input := []any{3, 1, 2}
	got, err := canonicalJSON(input)
	if err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}
	if string(got) != "[3,1,2]" {
		t.Errorf("array order changed: %s", got)
	}
}

func TestCanonicalJSON_NullAndBool(t *testing.T) {
	got, err := canonicalJSON(map[string]any{
		"is_null":  nil,
		"is_true":  true,
		"is_false": false,
	})
	if err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}
	if string(got) != `{"is_false":false,"is_null":null,"is_true":true}` {
		t.Errorf("got: %s", got)
	}
}

func TestComputeHash_DifferentInputsProduceDifferentHashes(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	base := Entry{
		ID:        "aud-test",
		Timestamp: now,
		Type:      "grant.create",
		Actor:     Actor{Kind: ActorUser, ID: "fortunatus"},
		Outcome:   OutcomeSuccess,
		PrevHash:  GenesisHash,
	}

	h1, err := computeHash(GenesisHash, base)
	if err != nil {
		t.Fatalf("computeHash: %v", err)
	}

	mutated := base
	mutated.Type = "grant.revoke"
	h2, err := computeHash(GenesisHash, mutated)
	if err != nil {
		t.Fatalf("computeHash mutated: %v", err)
	}

	if h1 == h2 {
		t.Errorf("different types should produce different hashes, got both %s", h1)
	}
	if !strings.HasPrefix(h1, "sha256:") {
		t.Errorf("hash should be prefixed with sha256:, got %s", h1)
	}
}

func TestComputeHash_IdenticalInputsProduceIdenticalHashes(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := Entry{
		ID:        "aud-test",
		Timestamp: now,
		Type:      "grant.create",
		Actor:     Actor{Kind: ActorUser, ID: "fortunatus"},
		Outcome:   OutcomeSuccess,
		PrevHash:  GenesisHash,
		Data:      map[string]any{"capability": "email.read", "scope": map[string]any{"sender": "sarah"}},
	}

	h1, _ := computeHash(GenesisHash, e)
	h2, _ := computeHash(GenesisHash, e)
	if h1 != h2 {
		t.Errorf("deterministic input should produce identical hash, got %s vs %s", h1, h2)
	}
}

func TestComputeHash_IgnoresOwnHashField(t *testing.T) {
	// The function explicitly zeroes the Hash field before
	// computing — the entry's existing Hash must not influence
	// the result.
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := Entry{Type: "x", Actor: Actor{Kind: ActorSystem, ID: "r"}, Outcome: OutcomeNA, Timestamp: now, PrevHash: GenesisHash, Hash: ""}
	b := a
	b.Hash = "sha256:fakefakefake"

	h1, _ := computeHash(GenesisHash, a)
	h2, _ := computeHash(GenesisHash, b)
	if h1 != h2 {
		t.Errorf("Hash field affected computeHash output: %s vs %s", h1, h2)
	}
}

func TestCanonicalJSON_MarshalRoundTrip(t *testing.T) {
	// Sanity: canonical output is still valid JSON the standard
	// library can parse back.
	input := map[string]any{"a": 1, "b": "x", "c": []any{1, 2}}
	c, err := canonicalJSON(input)
	if err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(c, &back); err != nil {
		t.Fatalf("unmarshal canonical: %v\nbytes: %s", err, c)
	}
}
