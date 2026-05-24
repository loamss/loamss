package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/loamss/loamss/runtime/internal/source"
)

// --- fake Gmail backend ----------------------------------------------

// fakeGmail implements just enough of the Gmail v1 API for tests:
//
//	/users/me/profile     → 200 {}
//	/users/me/messages    → paged list of message ids
//	/users/me/messages/N  → full message in raw format
//	/users/me/history     → history delta
//
// Tests configure it by populating Messages and HistoryEvents.
type fakeGmail struct {
	srv      *httptest.Server
	requests atomic.Int64

	// Message corpus keyed by id.
	messages  map[string]fakeMsg
	listOrder []string // ids returned by messages.list

	// History keyed by startHistoryId → list of events.
	histories map[string][]historyEvent

	// Auth control. Set to true to make the next API call return
	// 401, forcing the token-refresh path. Resets after one use.
	requireRefresh atomic.Bool

	// Pages first call → returns nextPageToken="page2", second call
	// returns the rest.
	paged bool

	// 429 control: how many requests until we stop returning 429.
	rateLimitFor atomic.Int64
}

type fakeMsg struct {
	ID        string
	ThreadID  string
	HistoryID string
	Snippet   string
	RawRFC822 string
	Labels    []string
}

type historyEvent struct {
	id      string
	added   []string
	deleted []string
}

func newFakeGmail() *fakeGmail {
	f := &fakeGmail{
		messages:  map[string]fakeMsg{},
		histories: map[string][]historyEvent{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeGmail) Close() { f.srv.Close() }

func (f *fakeGmail) handle(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)

	// Rate-limit control.
	if remaining := f.rateLimitFor.Load(); remaining > 0 {
		f.rateLimitFor.Add(-1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
		return
	}

	// Auth check.
	if r.Header.Get("Authorization") == "" {
		http.Error(w, "no auth", http.StatusUnauthorized)
		return
	}
	if f.requireRefresh.CompareAndSwap(true, false) {
		http.Error(w, "token expired", http.StatusUnauthorized)
		return
	}

	switch {
	case r.URL.Path == "/users/me/profile":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"emailAddress":"test@example.com"}`))
	case r.URL.Path == "/users/me/messages":
		f.serveList(w, r)
	case strings.HasPrefix(r.URL.Path, "/users/me/messages/"):
		id := strings.TrimPrefix(r.URL.Path, "/users/me/messages/")
		f.serveGet(w, id)
	case r.URL.Path == "/users/me/history":
		f.serveHistory(w, r)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (f *fakeGmail) serveList(w http.ResponseWriter, r *http.Request) {
	page := r.URL.Query().Get("pageToken")
	resp := listMessagesResponse{ResultSizeEstimate: len(f.listOrder)}

	if f.paged && page == "" {
		for _, id := range f.listOrder[:len(f.listOrder)/2] {
			resp.Messages = append(resp.Messages, struct{ ID, ThreadID string }{
				ID: id, ThreadID: f.messages[id].ThreadID,
			})
		}
		resp.NextPageToken = "page2"
	} else {
		start := 0
		if f.paged && page == "page2" {
			start = len(f.listOrder) / 2
		}
		for _, id := range f.listOrder[start:] {
			resp.Messages = append(resp.Messages, struct{ ID, ThreadID string }{
				ID: id, ThreadID: f.messages[id].ThreadID,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	body, _ := json.Marshal(resp)
	_, _ = w.Write(body)
}

func (f *fakeGmail) serveGet(w http.ResponseWriter, id string) {
	m, ok := f.messages[id]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	resp := getMessageResponse{
		ID:           m.ID,
		ThreadID:     m.ThreadID,
		LabelIDs:     m.Labels,
		Snippet:      m.Snippet,
		HistoryID:    m.HistoryID,
		InternalDate: strconv.FormatInt(time.Now().UnixMilli(), 10),
		SizeEstimate: len(m.RawRFC822),
		Raw:          base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString([]byte(m.RawRFC822)),
	}
	w.Header().Set("Content-Type", "application/json")
	body, _ := json.Marshal(resp)
	_, _ = w.Write(body)
}

func (f *fakeGmail) serveHistory(w http.ResponseWriter, r *http.Request) {
	startID := r.URL.Query().Get("startHistoryId")
	events := f.histories[startID]
	var hr listHistoryResponse
	highest := startID
	for _, ev := range events {
		entry := struct {
			ID              string                             `json:"id"`
			MessagesAdded   []struct{ Message historyMessage } `json:"messagesAdded,omitempty"`
			MessagesDeleted []struct{ Message historyMessage } `json:"messagesDeleted,omitempty"`
		}{ID: ev.id}
		for _, id := range ev.added {
			entry.MessagesAdded = append(entry.MessagesAdded,
				struct{ Message historyMessage }{Message: historyMessage{ID: id}})
		}
		for _, id := range ev.deleted {
			entry.MessagesDeleted = append(entry.MessagesDeleted,
				struct{ Message historyMessage }{Message: historyMessage{ID: id}})
		}
		hr.History = append(hr.History, entry)
		if ev.id > highest {
			highest = ev.id
		}
	}
	hr.HistoryID = highest

	w.Header().Set("Content-Type", "application/json")
	body, _ := json.Marshal(hr)
	_, _ = w.Write(body)
}

// --- helper: seed a token --------------------------------------------

func seedToken(t *testing.T, g *gmailSource, expiry time.Time) {
	t.Helper()
	tok := &oauthToken{
		AccessToken:  "ya29.test",
		RefreshToken: "1//refresh",
		Expiry:       expiry,
	}
	if err := g.saveToken(context.Background(), tok); err != nil {
		t.Fatalf("saveToken: %v", err)
	}
}

func sampleRFC822(subject, from string) string {
	return fmt.Sprintf("From: %s\r\nTo: dest@example.com\r\nSubject: %s\r\nDate: Mon, 24 May 2026 12:00:00 -0700\r\nMessage-ID: <%s@x>\r\nContent-Type: text/plain\r\n\r\nbody here\r\n",
		from, subject, subject)
}

// --- HealthCheck -----------------------------------------------------

func TestHealthCheck_OK(t *testing.T) {
	gm := newFakeGmail()
	defer gm.Close()

	// Need a token endpoint too (HealthCheck doesn't refresh
	// unless expired, but Init wires both).
	g, _, _, _ := newTestSource(t, "", "", gm.srv.URL)
	seedToken(t, g, time.Now().Add(time.Hour))

	if err := g.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
}

func TestHealthCheck_NoCredsOK(t *testing.T) {
	gm := newFakeGmail()
	defer gm.Close()
	g, _, _, _ := newTestSource(t, "", "", gm.srv.URL)

	// HealthCheck with no creds returns nil — it's a known state.
	if err := g.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck no creds: %v", err)
	}
}

// --- Full sync -------------------------------------------------------

func TestSync_FirstRun_FullSweep(t *testing.T) {
	gm := newFakeGmail()
	defer gm.Close()

	// Seed three messages.
	for _, id := range []string{"m1", "m2", "m3"} {
		gm.messages[id] = fakeMsg{
			ID:        id,
			ThreadID:  "t-" + id,
			HistoryID: "100" + id[1:],
			Snippet:   "snippet-" + id,
			RawRFC822: sampleRFC822("subj-"+id, "alice@example.com"),
			Labels:    []string{"INBOX"},
		}
		gm.listOrder = append(gm.listOrder, id)
	}

	g, storage, memory, _ := newTestSource(t, "", "", gm.srv.URL)
	seedToken(t, g, time.Now().Add(time.Hour))

	result, err := g.Sync(context.Background(), nil)
	if err != nil {
		t.Fatalf("Sync: %v\nresult=%+v", err, result)
	}
	if result.RecordsAdded != 3 {
		t.Errorf("RecordsAdded: %d", result.RecordsAdded)
	}
	for _, id := range []string{"m1", "m2", "m3"} {
		if !storage.has("sources/gmail-test/messages/" + id + ".eml") {
			t.Errorf("storage missing %s", id)
		}
		if _, ok := memory.get("gmail-test", id); !ok {
			t.Errorf("memory missing %s", id)
		}
	}

	cur, err := decodeCursor(result.Cursor)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if cur.HistoryID == "" {
		t.Error("cursor history_id empty")
	}
}

func TestSync_RespectsMaxFullSync(t *testing.T) {
	gm := newFakeGmail()
	defer gm.Close()

	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("m%02d", i)
		gm.messages[id] = fakeMsg{
			ID: id, ThreadID: "t", HistoryID: fmt.Sprintf("%03d", 100+i),
			RawRFC822: sampleRFC822("s-"+id, "x@y"),
		}
		gm.listOrder = append(gm.listOrder, id)
	}

	g, _, _, _ := newTestSource(t, "", "", gm.srv.URL)
	// newTestSource sets max_full_sync=10.
	seedToken(t, g, time.Now().Add(time.Hour))

	result, err := g.Sync(context.Background(), nil)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.RecordsAdded != 10 {
		t.Errorf("RecordsAdded: got %d, want 10 (max_full_sync cap)", result.RecordsAdded)
	}
}

func TestSync_Paginated(t *testing.T) {
	gm := newFakeGmail()
	gm.paged = true
	defer gm.Close()
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("m%d", i)
		gm.messages[id] = fakeMsg{
			ID: id, ThreadID: "t", HistoryID: fmt.Sprintf("%d", 200+i),
			RawRFC822: sampleRFC822("s", "x@y"),
		}
		gm.listOrder = append(gm.listOrder, id)
	}
	g, _, _, _ := newTestSource(t, "", "", gm.srv.URL)
	seedToken(t, g, time.Now().Add(time.Hour))

	result, err := g.Sync(context.Background(), nil)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.RecordsAdded != 6 {
		t.Errorf("paged sync RecordsAdded: %d", result.RecordsAdded)
	}
}

// --- Incremental sync ------------------------------------------------

func TestSync_Incremental_AddsAndDeletes(t *testing.T) {
	gm := newFakeGmail()
	defer gm.Close()

	// Pre-existing message in storage that history says was deleted.
	gm.messages["new1"] = fakeMsg{
		ID: "new1", ThreadID: "t", HistoryID: "300",
		RawRFC822: sampleRFC822("new", "x@y"),
	}
	gm.histories["100"] = []historyEvent{
		{id: "200", added: []string{"new1"}},
		{id: "210", deleted: []string{"old1"}},
	}

	g, storage, memory, _ := newTestSource(t, "", "", gm.srv.URL)
	seedToken(t, g, time.Now().Add(time.Hour))

	// Pre-seed storage + memory for "old1" so we can verify deletion.
	_ = storage.Write(context.Background(), "sources/gmail-test/messages/old1.eml", []byte("old"))
	_ = memory.Upsert(context.Background(), source.MemoryEntry{Namespace: "gmail-test", ID: "old1"})

	startCur := mustEncodeCursor(cursorPayload{HistoryID: "100"})
	result, err := g.Sync(context.Background(), startCur)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.RecordsUpdated != 1 {
		t.Errorf("RecordsUpdated: %d", result.RecordsUpdated)
	}
	if !storage.has("sources/gmail-test/messages/new1.eml") {
		t.Error("new1 not stored")
	}
	if storage.has("sources/gmail-test/messages/old1.eml") {
		t.Error("old1 should have been deleted")
	}
	if _, ok := memory.get("gmail-test", "old1"); ok {
		t.Error("old1 still in memory")
	}

	cur, _ := decodeCursor(result.Cursor)
	if cur.HistoryID != "210" {
		t.Errorf("cursor: got %q, want 210", cur.HistoryID)
	}
}

// --- Token refresh on 401 -------------------------------------------

func TestSync_RefreshesOn401(t *testing.T) {
	gm := newFakeGmail()
	defer gm.Close()
	gm.messages["m1"] = fakeMsg{
		ID: "m1", ThreadID: "t", HistoryID: "100",
		RawRFC822: sampleRFC822("s", "x@y"),
	}
	gm.listOrder = []string{"m1"}

	// Token endpoint must return a fresh token on the refresh call.
	fts := newFakeTokenServer(t, map[string]any{
		"access_token": "ya29.fresh-after-401",
		"expires_in":   3600,
		"token_type":   "Bearer",
	}, 200)

	g, _, _, _ := newTestSource(t, "", fts.srv.URL, gm.srv.URL)
	seedToken(t, g, time.Now().Add(time.Hour)) // not expired client-side

	gm.requireRefresh.Store(true) // first API call → 401

	result, err := g.Sync(context.Background(), nil)
	if err != nil {
		t.Fatalf("Sync after refresh: %v", err)
	}
	if result.RecordsAdded != 1 {
		t.Errorf("RecordsAdded: %d", result.RecordsAdded)
	}
	tok, _ := g.loadToken(context.Background())
	if tok.AccessToken != "ya29.fresh-after-401" {
		t.Errorf("token not refreshed: %q", tok.AccessToken)
	}
}

func TestSync_RetriesAfter429(t *testing.T) {
	gm := newFakeGmail()
	defer gm.Close()
	gm.messages["m1"] = fakeMsg{ID: "m1", HistoryID: "100", RawRFC822: sampleRFC822("s", "x@y")}
	gm.listOrder = []string{"m1"}
	gm.rateLimitFor.Store(1) // first request rate-limited

	g, _, _, _ := newTestSource(t, "", "", gm.srv.URL)
	seedToken(t, g, time.Now().Add(time.Hour))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := g.Sync(ctx, nil)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.RecordsAdded != 1 {
		t.Errorf("RecordsAdded: %d", result.RecordsAdded)
	}
}

// --- Message → memory entry shape -----------------------------------

func TestMessageToMemoryEntry_ExtractsHeaders(t *testing.T) {
	raw := []byte("From: alice@example.com\r\nTo: bob@example.com\r\nSubject: Project Alpha\r\nDate: Mon, 24 May 2026 12:00:00 -0700\r\nMessage-ID: <abc123@mail>\r\n\r\nbody\r\n")
	gm := getMessageResponse{
		ID: "msg-1", ThreadID: "thr-1", HistoryID: "777",
		Snippet:  "Project Alpha discussion",
		LabelIDs: []string{"INBOX", "IMPORTANT"},
	}
	entry, err := messageToMemoryEntry("gmail-personal", gm, raw)
	if err != nil {
		t.Fatalf("messageToMemoryEntry: %v", err)
	}
	if entry.Namespace != "gmail-personal" {
		t.Errorf("namespace: %q", entry.Namespace)
	}
	if entry.ID != "msg-1" {
		t.Errorf("id: %q", entry.ID)
	}
	if !strings.Contains(entry.Content, "Project Alpha") {
		t.Errorf("content missing subject: %q", entry.Content)
	}
	if entry.Metadata["from"] != "alice@example.com" {
		t.Errorf("from: %v", entry.Metadata["from"])
	}
	if entry.Metadata["gmail_thread_id"] != "thr-1" {
		t.Errorf("thread_id: %v", entry.Metadata["gmail_thread_id"])
	}
	if entry.Metadata["rfc822_message_id"] != "abc123@mail" {
		t.Errorf("rfc822 message id: %v", entry.Metadata["rfc822_message_id"])
	}
}

// --- helpers ---------------------------------------------------------

func init() {
	// Silence the default Gmail backoff for tests by making the fallback
	// sleep nearly nothing — we use Retry-After: 1 in the rate-limit
	// test but the fake's handler decrements quickly anyway.
	_ = io.Discard
}
