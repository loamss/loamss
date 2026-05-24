package memory

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestExtractEntities_GmailShape(t *testing.T) {
	entry := Entry{
		Namespace: "gmail-personal",
		ID:        "msg-1",
		Metadata: map[string]any{
			"from":          `"Sarah Smith" <sarah@example.com>`,
			"to":            `bob@example.com, "Alice Wonderland" <alice@example.com>`,
			"cc":            `<carol@example.com>`,
			"internal_date": "2026-05-24T12:00:00Z",
		},
	}
	ext := ExtractEntities(entry)
	if ext.EntryDate.IsZero() {
		t.Error("EntryDate not parsed from internal_date")
	}

	type row struct {
		canonical string
		email     string
		role      EntryRole
	}
	var got []row
	for _, e := range ext.Entities {
		emailAlias := ""
		for _, a := range e.Entity.Aliases {
			if a.Kind == AliasKindEmail {
				emailAlias = a.Value
				break
			}
		}
		got = append(got, row{
			canonical: e.Entity.Canonical,
			email:     emailAlias,
			role:      e.Role,
		})
	}
	sort.Slice(got, func(i, j int) bool {
		if got[i].role != got[j].role {
			return got[i].role < got[j].role
		}
		return got[i].email < got[j].email
	})

	want := []row{
		{"Sarah Smith", "sarah@example.com", RoleFrom},
		{"bob", "bob@example.com", RoleTo},
		{"Alice Wonderland", "alice@example.com", RoleTo},
		{"carol", "carol@example.com", RoleCC},
	}
	sort.Slice(want, func(i, j int) bool {
		if want[i].role != want[j].role {
			return want[i].role < want[j].role
		}
		return want[i].email < want[j].email
	})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entities mismatch.\ngot:  %+v\nwant: %+v", got, want)
	}
}

func TestExtractEntities_LowercasesEmails(t *testing.T) {
	entry := Entry{
		Namespace: "ns",
		ID:        "x",
		Metadata:  map[string]any{"from": "SaRaH@Example.COM"},
	}
	ext := ExtractEntities(entry)
	if len(ext.Entities) != 1 {
		t.Fatalf("got %d entities", len(ext.Entities))
	}
	if ext.Entities[0].Entity.Aliases[0].Value != "sarah@example.com" {
		t.Errorf("email not lowercased: %q", ext.Entities[0].Entity.Aliases[0].Value)
	}
}

func TestExtractEntities_NoMetadata(t *testing.T) {
	ext := ExtractEntities(Entry{Namespace: "ns", ID: "x"})
	if len(ext.Entities) != 0 {
		t.Errorf("expected 0 entities, got %d", len(ext.Entities))
	}
}

func TestExtractEntities_MalformedAddressTolerated(t *testing.T) {
	entry := Entry{
		Namespace: "ns",
		ID:        "x",
		Metadata: map[string]any{
			"from": "not-an-email",
			"to":   "valid@example.com",
		},
	}
	ext := ExtractEntities(entry)
	// "not-an-email" has no @; the fallback parser ignores it. We
	// should still get the valid To: address.
	if len(ext.Entities) != 1 {
		t.Errorf("expected 1 entity (the valid one), got %d", len(ext.Entities))
	}
}

func TestExtractEntities_BackToBackUpdatesCanonical(t *testing.T) {
	// First entry has no display name — canonical falls back to
	// local-part. Second entry adds the name. ShouldUpgradeCanonical
	// would replace "sarah" with "Sarah Smith".
	if !shouldUpgradeCanonical("sarah", "Sarah Smith") {
		t.Error("expected upgrade from local-part to real name")
	}
	if shouldUpgradeCanonical("Sarah Smith", "sarah") {
		t.Error("did not expect downgrade from real name to local-part")
	}
	if shouldUpgradeCanonical("", "anything") != true {
		t.Error("empty canonical should be upgraded")
	}
}

func TestExtractThread_GmailThreadID(t *testing.T) {
	entry := Entry{
		Namespace: "gmail-personal",
		ID:        "msg-1",
		Metadata: map[string]any{
			"gmail_thread_id": "thr-abc-123",
			"subject":         "Project Alpha discussion",
			"internal_date":   "2026-05-24T12:00:00Z",
		},
	}
	ext := ExtractThread(entry)
	if ext.ExternalID != "thr-abc-123" {
		t.Errorf("ExternalID: %q", ext.ExternalID)
	}
	if ext.Subject != "Project Alpha discussion" {
		t.Errorf("Subject: %q", ext.Subject)
	}
	if ext.EntryDate.IsZero() {
		t.Error("EntryDate not parsed")
	}
}

func TestExtractThread_NoThreadID(t *testing.T) {
	ext := ExtractThread(Entry{
		Namespace: "ns",
		ID:        "x",
		Metadata:  map[string]any{"subject": "x"},
	})
	if ext.ExternalID != "" {
		t.Errorf("expected empty ExternalID, got %q", ext.ExternalID)
	}
}

func TestParseEntryDate_RFC1123Z(t *testing.T) {
	entry := map[string]any{
		"date_header": "Mon, 24 May 2026 12:00:00 -0700",
	}
	got := parseEntryDate(entry)
	if got.IsZero() {
		t.Fatal("expected parsed date")
	}
	want, _ := time.Parse(time.RFC1123Z, "Mon, 24 May 2026 12:00:00 -0700")
	if !got.Equal(want.UTC()) {
		t.Errorf("got %v, want %v", got, want.UTC())
	}
}

func TestParseAddresses_HandlesQuotedCommas(t *testing.T) {
	// Display name contains a comma, which would break naive
	// comma-splitting. mail.ParseAddressList handles this correctly.
	addrs := parseAddresses(`"Last, First" <a@b.com>, plain@c.com`)
	if len(addrs) != 2 {
		t.Fatalf("got %d addrs", len(addrs))
	}
	if addrs[0].Address != "a@b.com" || addrs[0].Name != "Last, First" {
		t.Errorf("first addr: %+v", addrs[0])
	}
	if addrs[1].Address != "plain@c.com" {
		t.Errorf("second addr: %+v", addrs[1])
	}
}

func TestExplainNoEntities(t *testing.T) {
	got := ExplainNoEntities(Entry{Namespace: "ns", ID: "x"})
	if !strings.Contains(got, "no metadata") {
		t.Errorf("expected 'no metadata' explanation, got %q", got)
	}
	got = ExplainNoEntities(Entry{
		Namespace: "ns", ID: "x",
		Metadata: map[string]any{"subject": "x"},
	})
	if !strings.Contains(got, "no from/to") {
		t.Errorf("expected 'no from/to' explanation, got %q", got)
	}
}
