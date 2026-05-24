package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// pairAClient walks the engine through pair → redeem and returns
// the resulting bearer token and client id. Used by auth/MCP tests
// that need a real, authenticatable bearer.
func pairAClient(t *testing.T, d *fullDeps, name string) (token, clientID string) {
	t.Helper()
	ctx := context.Background()
	p, err := d.engine.CreatePairingCode(ctx, name, "user", time.Hour)
	if err != nil {
		t.Fatalf("CreatePairingCode: %v", err)
	}
	c, tok, err := d.engine.RedeemPairingCode(ctx, p.Code, nil)
	if err != nil {
		t.Fatalf("RedeemPairingCode: %v", err)
	}
	return tok, c.ID
}

func TestAuth_AllowsValidBearer(t *testing.T) {
	d := newFullDeps(t)
	base, stop := startFullServer(t, d)
	defer stop()

	token, _ := pairAClient(t, d, "claude")

	// Even with no MCP tools registered, initialize should work.
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "0"},
		},
	})
	req, _ := http.NewRequest(http.MethodPost, base+"/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAuth_RejectsMissingHeader(t *testing.T) {
	d := newFullDeps(t)
	base, stop := startFullServer(t, d)
	defer stop()

	resp, err := http.Post(base+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Error("expected WWW-Authenticate challenge header")
	}
}

func TestAuth_RejectsMalformedHeader(t *testing.T) {
	d := newFullDeps(t)
	base, stop := startFullServer(t, d)
	defer stop()

	for _, header := range []string{
		"BasicAbc==",        // wrong scheme
		"Bearer",            // no token
		"Bearer ",           // empty token
		"Bear lck_xyz_abc",  // truncated scheme
		"NotBearer lck_xyz", // other scheme
		"",                  // no header at all (handled above too)
	} {
		req, _ := http.NewRequest(http.MethodPost, base+"/mcp", strings.NewReader("{}"))
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("header %q: expected 401, got %d", header, resp.StatusCode)
		}
	}
}

func TestAuth_RejectsRevokedClient(t *testing.T) {
	d := newFullDeps(t)
	base, stop := startFullServer(t, d)
	defer stop()

	token, id := pairAClient(t, d, "doomed")
	if err := d.engine.RevokeClient(context.Background(), id, "user", "test"); err != nil {
		t.Fatalf("RevokeClient: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, base+"/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for revoked client, got %d", resp.StatusCode)
	}
}

func TestExtractBearerToken_Cases(t *testing.T) {
	cases := []struct {
		header  string
		want    string
		wantErr bool
	}{
		{"", "", true},
		{"x", "", true},
		{"Bearer", "", true},
		{"Bearer ", "", true},
		{"Bearer abc", "abc", false},
		{"bearer abc", "abc", false}, // case-insensitive scheme
		{"BEARER  abc  ", "abc", false},
		{"Basic abc", "", true},
	}
	for _, tc := range cases {
		got, err := extractBearerToken(tc.header)
		if tc.wantErr && err == nil {
			t.Errorf("header %q: expected error", tc.header)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("header %q: unexpected error %v", tc.header, err)
		}
		if got != tc.want {
			t.Errorf("header %q: got %q want %q", tc.header, got, tc.want)
		}
	}
}
