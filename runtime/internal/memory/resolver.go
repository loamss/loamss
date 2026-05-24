package memory

import (
	"net/mail"
	"strings"
	"time"
)

// EntityExtraction is the result of running an entry through the
// resolver. The Layer turns this into upsert+link operations.
type EntityExtraction struct {
	// Entities to upsert. Each has aliases populated; the store
	// merges these against any existing entity in the same namespace.
	Entities []ExtractedEntity

	// EntryDate is the entry's "happened-at" timestamp if the
	// resolver could find one (from a Date header, internalDate,
	// etc.). Used to backfill FirstSeen/LastSeen and to order
	// entries on read.
	EntryDate time.Time
}

// ExtractedEntity bundles an Entity proposal with the role it played
// in the entry (so the Layer can link it correctly).
type ExtractedEntity struct {
	Entity Entity
	Role   EntryRole
}

// ThreadExtraction describes the thread (if any) an entry belongs to.
type ThreadExtraction struct {
	// ExternalID is empty when the entry has no thread context.
	ExternalID string
	Subject    string
	EntryDate  time.Time
}

// ExtractEntities runs the v0.1 resolver on a memory entry. Today
// this understands Gmail-shaped metadata (the same fields
// source:gmail writes). Other shapes return zero extractions until
// their resolvers land.
//
// Inputs read from entry.Metadata:
//
//	from              — RFC822 From: header (display name + address)
//	to                — RFC822 To: header (may contain multiple addresses)
//	cc                — RFC822 Cc: header (optional)
//	bcc               — RFC822 Bcc: header (optional)
//	internal_date     — RFC3339 timestamp; preferred over date_header
//	date_header       — RFC822 Date: header as fallback
//
// The namespace is taken from entry.Namespace so the same email
// address in two configured Gmail accounts ("gmail-personal" vs
// "gmail-work") resolves to two distinct entities — v0.1 doesn't
// cross-merge.
func ExtractEntities(entry Entry) EntityExtraction {
	out := EntityExtraction{EntryDate: parseEntryDate(entry.Metadata)}
	if entry.Metadata == nil {
		return out
	}

	from := stringFromMetadata(entry.Metadata, "from")
	to := stringFromMetadata(entry.Metadata, "to")
	cc := stringFromMetadata(entry.Metadata, "cc")
	bcc := stringFromMetadata(entry.Metadata, "bcc")

	for _, addr := range parseAddresses(from) {
		out.Entities = append(out.Entities, makeExtraction(entry.Namespace, addr, RoleFrom))
	}
	for _, addr := range parseAddresses(to) {
		out.Entities = append(out.Entities, makeExtraction(entry.Namespace, addr, RoleTo))
	}
	for _, addr := range parseAddresses(cc) {
		out.Entities = append(out.Entities, makeExtraction(entry.Namespace, addr, RoleCC))
	}
	for _, addr := range parseAddresses(bcc) {
		out.Entities = append(out.Entities, makeExtraction(entry.Namespace, addr, RoleBCC))
	}
	return out
}

func makeExtraction(namespace string, addr *mail.Address, role EntryRole) ExtractedEntity {
	canonical := strings.TrimSpace(addr.Name)
	if canonical == "" {
		canonical = emailLocalPart(addr.Address)
	}
	aliases := []Alias{{Value: strings.ToLower(addr.Address), Kind: AliasKindEmail}}
	if addr.Name != "" {
		aliases = append(aliases,
			Alias{Value: strings.TrimSpace(addr.Name), Kind: AliasKindName})
	}
	return ExtractedEntity{
		Entity: Entity{
			Kind:      EntityPerson,
			Canonical: canonical,
			Namespace: namespace,
			Aliases:   aliases,
		},
		Role: role,
	}
}

// ExtractThread runs the v0.1 thread resolver. Today this recognizes
// only the Gmail thread_id convention.
//
// Inputs read from entry.Metadata:
//
//	gmail_thread_id  — opaque Gmail-assigned thread identifier
//	subject          — used as the thread label
//	internal_date / date_header — for ordering
func ExtractThread(entry Entry) ThreadExtraction {
	if entry.Metadata == nil {
		return ThreadExtraction{}
	}
	tid := stringFromMetadata(entry.Metadata, "gmail_thread_id")
	if tid == "" {
		return ThreadExtraction{}
	}
	return ThreadExtraction{
		ExternalID: tid,
		Subject:    stringFromMetadata(entry.Metadata, "subject"),
		EntryDate:  parseEntryDate(entry.Metadata),
	}
}

// --- parsing helpers --------------------------------------------------

// parseAddresses tolerates the messy real-world content of RFC822
// address headers — multiple addresses, quoted names with commas,
// MIME-encoded display names, missing display names. We use the
// stdlib's mail.ParseAddressList; on parse failure we fall back to a
// best-effort split.
func parseAddresses(h string) []*mail.Address {
	h = strings.TrimSpace(h)
	if h == "" {
		return nil
	}
	if addrs, err := mail.ParseAddressList(h); err == nil {
		return addrs
	}
	// Fallback: split on comma, parse each piece.
	var out []*mail.Address
	for _, piece := range strings.Split(h, ",") {
		piece = strings.TrimSpace(piece)
		if piece == "" {
			continue
		}
		if a, err := mail.ParseAddress(piece); err == nil {
			out = append(out, a)
			continue
		}
		// Last resort: treat as bare email.
		if strings.Contains(piece, "@") {
			out = append(out, &mail.Address{Address: piece})
		}
	}
	return out
}

func emailLocalPart(addr string) string {
	at := strings.IndexByte(addr, '@')
	if at <= 0 {
		return addr
	}
	return addr[:at]
}

func parseEntryDate(metadata map[string]any) time.Time {
	if metadata == nil {
		return time.Time{}
	}
	if s := stringFromMetadata(metadata, "internal_date"); s != "" {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.UTC()
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.UTC()
		}
	}
	if s := stringFromMetadata(metadata, "date_header"); s != "" {
		// RFC1123Z is what most Date: headers actually look like.
		for _, layout := range []string{
			time.RFC1123Z,
			time.RFC1123,
			"Mon, 2 Jan 2006 15:04:05 -0700",
			"Mon, 2 Jan 2006 15:04:05 MST",
		} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC()
			}
		}
	}
	return time.Time{}
}

func stringFromMetadata(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
