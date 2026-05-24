package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/loamss/loamss/runtime/internal/source"
)

// gmailAPI is the per-sync client. Holds a back-reference to the
// gmailSource so it can refresh tokens transparently and use the
// configured URLs + HTTP client.
type gmailAPI struct {
	src *gmailSource
}

func (g *gmailSource) newAPIClient() *gmailAPI {
	return &gmailAPI{src: g}
}

// --- request envelope --------------------------------------------------

// do performs a token-aware GET. Handles one-shot 401-retry-after-
// refresh and 429 rate-limit backoff.
func (a *gmailAPI) do(ctx context.Context, path string) ([]byte, error) {
	for attempt := 0; attempt < 3; attempt++ {
		tok, err := a.src.loadToken(ctx)
		if err != nil {
			return nil, err
		}
		if tok.expired() || tok.expiresSoon() {
			if err := a.src.refreshToken(ctx, tok); err != nil {
				return nil, err
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.src.apiBase+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		req.Header.Set("Accept", "application/json")

		resp, err := a.src.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK:
			return body, nil
		case http.StatusUnauthorized:
			// Token rejected — force refresh and retry once.
			if attempt > 0 {
				return nil, fmt.Errorf("gmail: 401 unauthorized after refresh: %s", snippet(body))
			}
			tok.Expiry = time.Now().Add(-time.Minute) // force expired
			if err := a.src.refreshToken(ctx, tok); err != nil {
				return nil, fmt.Errorf("gmail: refreshing after 401: %w", err)
			}
		case http.StatusTooManyRequests:
			wait := parseRetryAfter(resp.Header.Get("Retry-After"), 2*time.Second)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		case http.StatusNotFound:
			return nil, fmt.Errorf("gmail: 404 %s", path)
		default:
			return nil, fmt.Errorf("gmail: %d %s — %s", resp.StatusCode, path, snippet(body))
		}
	}
	return nil, errors.New("gmail: exhausted retries")
}

func parseRetryAfter(h string, fallback time.Duration) time.Duration {
	if h == "" {
		return fallback
	}
	if secs, err := strconv.Atoi(h); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return fallback
}

// --- profile / health -------------------------------------------------

func (a *gmailAPI) profilePing(ctx context.Context) error {
	_, err := a.do(ctx, "/users/me/profile")
	return err
}

// --- list / get / history wire types ---------------------------------

type listMessagesResponse struct {
	Messages           []struct{ ID, ThreadID string } `json:"messages"`
	NextPageToken      string                          `json:"nextPageToken,omitempty"`
	ResultSizeEstimate int                             `json:"resultSizeEstimate"`
}

type getMessageResponse struct {
	ID           string   `json:"id"`
	ThreadID     string   `json:"threadId"`
	LabelIDs     []string `json:"labelIds"`
	Snippet      string   `json:"snippet"`
	HistoryID    string   `json:"historyId"`
	InternalDate string   `json:"internalDate"`
	SizeEstimate int      `json:"sizeEstimate"`
	Raw          string   `json:"raw"` // base64url of full RFC822
}

type listHistoryResponse struct {
	History []struct {
		ID              string                             `json:"id"`
		MessagesAdded   []struct{ Message historyMessage } `json:"messagesAdded,omitempty"`
		MessagesDeleted []struct{ Message historyMessage } `json:"messagesDeleted,omitempty"`
	} `json:"history,omitempty"`
	NextPageToken string `json:"nextPageToken,omitempty"`
	HistoryID     string `json:"historyId,omitempty"`
}

type historyMessage struct {
	ID       string `json:"id"`
	ThreadID string `json:"threadId"`
}

// --- full sync --------------------------------------------------------

func (g *gmailSource) fullSync(ctx context.Context, api *gmailAPI, result *source.SyncResult) (string, error) {
	pageToken := ""
	collected := 0
	highestHistory := ""

	for collected < g.maxFullSync {
		path := "/users/me/messages?maxResults=100"
		if pageToken != "" {
			path += "&pageToken=" + pageToken
		}
		if g.query != "" {
			path += "&q=" + queryEscape(g.query)
		}
		body, err := api.do(ctx, path)
		if err != nil {
			return highestHistory, fmt.Errorf("listing messages: %w", err)
		}
		var lr listMessagesResponse
		if err := json.Unmarshal(body, &lr); err != nil {
			return highestHistory, fmt.Errorf("decoding messages.list: %w", err)
		}
		for _, m := range lr.Messages {
			if collected >= g.maxFullSync {
				break
			}
			h, err := g.ingestMessageReturnHistory(ctx, api, m.ID, result)
			if err != nil {
				result.Errors = append(result.Errors, source.SyncError{
					RecordID: m.ID,
					Reason:   err.Error(),
				})
				continue
			}
			if h > highestHistory {
				highestHistory = h
			}
			collected++
		}
		pageToken = lr.NextPageToken
		if pageToken == "" {
			break
		}
	}
	return highestHistory, nil
}

// --- per-message ingest ----------------------------------------------

func (g *gmailSource) ingestMessage(ctx context.Context, api *gmailAPI, messageID string, result *source.SyncResult) error {
	_, err := g.ingestMessageReturnHistory(ctx, api, messageID, result)
	return err
}

// ingestMessageReturnHistory fetches one message in raw format,
// writes the EML to storage, and upserts a normalized memory entry.
// Returns the message's historyId so the caller can track the
// high-water mark.
func (g *gmailSource) ingestMessageReturnHistory(ctx context.Context, api *gmailAPI, messageID string, result *source.SyncResult) (string, error) {
	body, err := api.do(ctx, "/users/me/messages/"+messageID+"?format=raw")
	if err != nil {
		return "", err
	}
	var gm getMessageResponse
	if err := json.Unmarshal(body, &gm); err != nil {
		return "", fmt.Errorf("decoding messages.get: %w", err)
	}

	raw, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(gm.Raw)
	if err != nil {
		// Gmail uses URL-safe base64 without padding; some libraries
		// emit padding anyway. Fall back to padded decoding.
		raw, err = base64.URLEncoding.DecodeString(gm.Raw)
		if err != nil {
			return "", fmt.Errorf("decoding raw RFC822 (base64): %w", err)
		}
	}

	storagePath := messagePath(g.deps.SourceName, gm.ID)
	if err := g.deps.Storage.Write(ctx, storagePath, raw); err != nil {
		return "", fmt.Errorf("writing %s: %w", storagePath, err)
	}
	result.BytesIngested += int64(len(raw))
	result.RecordsAdded++

	entry, err := messageToMemoryEntry(g.deps.SourceName, gm, raw)
	if err != nil {
		// EML parsed badly; still keep the raw on disk but record
		// the parse failure as a per-record error.
		return gm.HistoryID, fmt.Errorf("parsing RFC822 for memory entry: %w", err)
	}
	if g.deps.Memory != nil {
		if err := g.deps.Memory.Upsert(ctx, entry); err != nil {
			return gm.HistoryID, fmt.Errorf("upserting memory entry: %w", err)
		}
	}
	return gm.HistoryID, nil
}

// --- history (incremental) -------------------------------------------

func (a *gmailAPI) listHistory(ctx context.Context, startHistoryID string) (added, deleted []string, newHistoryID string, err error) {
	pageToken := ""
	newHistoryID = startHistoryID
	for {
		path := "/users/me/history?startHistoryId=" + startHistoryID
		if pageToken != "" {
			path += "&pageToken=" + pageToken
		}
		body, err := a.do(ctx, path)
		if err != nil {
			return nil, nil, startHistoryID, err
		}
		var hr listHistoryResponse
		if err := json.Unmarshal(body, &hr); err != nil {
			return nil, nil, startHistoryID, fmt.Errorf("decoding history.list: %w", err)
		}
		if hr.HistoryID != "" && hr.HistoryID > newHistoryID {
			newHistoryID = hr.HistoryID
		}
		for _, h := range hr.History {
			if h.ID > newHistoryID {
				newHistoryID = h.ID
			}
			for _, m := range h.MessagesAdded {
				added = append(added, m.Message.ID)
			}
			for _, m := range h.MessagesDeleted {
				deleted = append(deleted, m.Message.ID)
			}
		}
		if hr.NextPageToken == "" {
			break
		}
		pageToken = hr.NextPageToken
	}
	return added, deleted, newHistoryID, nil
}

// --- RFC822 → memory entry -------------------------------------------

func messageToMemoryEntry(sourceName string, gm getMessageResponse, raw []byte) (source.MemoryEntry, error) {
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return source.MemoryEntry{}, err
	}
	header := msg.Header

	subject := header.Get("Subject")
	from := header.Get("From")
	to := header.Get("To")
	dateStr := header.Get("Date")
	messageIDHeader := header.Get("Message-ID")

	// Gmail's internalDate is ms-since-epoch; useful when the
	// Date: header is missing or skewed.
	var internalDate time.Time
	if gm.InternalDate != "" {
		if ms, err := strconv.ParseInt(gm.InternalDate, 10, 64); err == nil {
			internalDate = time.UnixMilli(ms).UTC()
		}
	}

	// Content kept compact: subject + snippet. Full-body parsing
	// (multipart/HTML stripping) is an organizer-capsule concern,
	// not a source's.
	content := strings.TrimSpace(subject)
	if gm.Snippet != "" {
		if content != "" {
			content += "\n\n"
		}
		content += strings.TrimSpace(gm.Snippet)
	}

	metadata := map[string]any{
		"source":            sourceName,
		"adapter_id":        SourceID,
		"gmail_message_id":  gm.ID,
		"gmail_thread_id":   gm.ThreadID,
		"gmail_history_id":  gm.HistoryID,
		"gmail_labels":      gm.LabelIDs,
		"size_estimate":     gm.SizeEstimate,
		"subject":           subject,
		"from":              from,
		"to":                to,
		"date_header":       dateStr,
		"rfc822_message_id": strings.Trim(messageIDHeader, "<>"),
	}
	if !internalDate.IsZero() {
		metadata["internal_date"] = internalDate.Format(time.RFC3339)
	}

	return source.MemoryEntry{
		Namespace: sourceName,
		ID:        gm.ID,
		Content:   content,
		Metadata:  metadata,
	}, nil
}

// --- cursor encoding -------------------------------------------------

type cursorPayload struct {
	HistoryID    string `json:"history_id,omitempty"`
	LastSyncTime string `json:"last_sync_time,omitempty"`
}

func decodeCursor(b []byte) (cursorPayload, error) {
	if len(b) == 0 {
		return cursorPayload{}, nil
	}
	var c cursorPayload
	if err := json.Unmarshal(b, &c); err != nil {
		return cursorPayload{}, err
	}
	return c, nil
}

func mustEncodeCursor(c cursorPayload) []byte {
	b, _ := json.Marshal(c)
	return b
}

// --- misc -------------------------------------------------------------

// queryEscape escapes Gmail search queries for the q= URL parameter.
// We use a minimal pass — the q syntax allows characters most
// url.QueryEscape would mangle.
func queryEscape(q string) string {
	r := strings.NewReplacer(
		" ", "+",
		"\"", "%22",
		"&", "%26",
		"=", "%3D",
		"#", "%23",
	)
	return r.Replace(q)
}
