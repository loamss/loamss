package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// GenesisHash is the sentinel value for the first entry's PrevHash.
// Subsequent entries chain back through their predecessors to this
// value; Verify walks back to it and stops there.
const GenesisHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// hashPrefix prefixes all chain hashes. Keeping it explicit means
// future hash-algorithm migrations are flagged in the data itself,
// not implicit in code.
const hashPrefix = "sha256:"

// computeHash returns the SHA-256 hash for entry e, chained from
// prevHash. The entry's own Hash field is ignored (the function
// works on a copy with Hash="").
//
// Hash = "sha256:" + hex(SHA-256(prevHash || canonical_json(entry_without_hash)))
//
// where canonical_json sorts object keys at every depth and uses no
// extraneous whitespace, so the same logical entry produces the same
// bytes regardless of in-memory layout.
func computeHash(prevHash string, e Entry) (string, error) {
	e.Hash = ""
	payload, err := canonicalJSON(e)
	if err != nil {
		return "", fmt.Errorf("audit: canonicalizing entry: %w", err)
	}
	h := sha256.New()
	h.Write([]byte(prevHash))
	h.Write(payload)
	return hashPrefix + hex.EncodeToString(h.Sum(nil)), nil
}

// canonicalJSON returns a deterministic JSON encoding of v: object
// keys sorted at every level, no insignificant whitespace, arrays
// preserving order. This is what we hash.
//
// Implementation: first round-trip through encoding/json to normalize
// numeric types (Go's float/int distinctions, etc.), then walk the
// generic representation and emit with sorted keys.
func canonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	var generic any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // preserve integer precision; avoids 64-bit ints losing precision via float64
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := writeCanonical(&buf, generic); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if x {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		raw, err := json.Marshal(x)
		if err != nil {
			return err
		}
		buf.Write(raw)
	case json.Number:
		buf.WriteString(x.String())
	case []any:
		buf.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			keyJSON, err := json.Marshal(k)
			if err != nil {
				return err
			}
			buf.Write(keyJSON)
			buf.WriteByte(':')
			if err := writeCanonical(buf, x[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		// Last resort: fall back to encoding/json. Shouldn't be reached
		// after UseNumber decoding above — all JSON-representable values
		// land in one of the cases above.
		return fmt.Errorf("audit: canonicalJSON: unsupported type %T", v)
	}
	return nil
}

// VerifyResult is what Verify reports. Valid is true only if the
// chain walks cleanly from genesis to head with every hash matching.
type VerifyResult struct {
	EntriesChecked int64  `json:"entries_checked"`
	Valid          bool   `json:"valid"`
	BrokenAt       string `json:"broken_at,omitempty"` // entry ID at the break, if any
	Reason         string `json:"reason,omitempty"`
}
