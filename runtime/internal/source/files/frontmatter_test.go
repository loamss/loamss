package files

import (
	"strings"
	"testing"
	"time"
)

func TestParseFile_NoFrontmatter(t *testing.T) {
	raw := []byte("# Just a markdown file\n\nWith no frontmatter.\n")
	p := parseFile(raw)
	if p.from != "" || p.subject != "" || !p.date.IsZero() {
		t.Errorf("expected empty parsed metadata, got %+v", p)
	}
	if !strings.HasPrefix(p.body, "# Just a markdown file") {
		t.Errorf("body should be unchanged when there is no frontmatter")
	}
}

func TestParseFile_FullFrontmatter(t *testing.T) {
	raw := []byte(`---
from: "Sarah Smith <sarah@example.com>"
to: Bob Lee <bob@example.com>
cc: carol@example.com
subject: "Project Alpha: kickoff notes"
thread: proj-alpha-001
date: 2026-05-20T10:00:00Z
priority: high
---

Body of the note goes here.
Multiple lines OK.
`)
	p := parseFile(raw)
	if p.from != "Sarah Smith <sarah@example.com>" {
		t.Errorf("from: %q", p.from)
	}
	if p.to != "Bob Lee <bob@example.com>" {
		t.Errorf("to: %q", p.to)
	}
	if p.cc != "carol@example.com" {
		t.Errorf("cc: %q", p.cc)
	}
	if p.subject != "Project Alpha: kickoff notes" {
		t.Errorf("subject: %q", p.subject)
	}
	if p.thread != "proj-alpha-001" {
		t.Errorf("thread: %q", p.thread)
	}
	want, _ := time.Parse(time.RFC3339, "2026-05-20T10:00:00Z")
	if !p.date.Equal(want) {
		t.Errorf("date: %v, want %v", p.date, want)
	}
	if p.extras["priority"] != "high" {
		t.Errorf("extras: %+v", p.extras)
	}
	if !strings.HasPrefix(p.body, "Body of the note goes here.") {
		t.Errorf("body: %q", p.body)
	}
}

func TestParseFile_TitleFallsBackToSubject(t *testing.T) {
	raw := []byte(`---
title: My note
---

Body.
`)
	p := parseFile(raw)
	if p.title != "My note" {
		t.Errorf("title: %q", p.title)
	}
	if p.subject != "" {
		t.Errorf("subject should not auto-set; got %q", p.subject)
	}
}

func TestParseFile_UnclosedFrontmatterTreatedAsBody(t *testing.T) {
	raw := []byte(`---
from: someone
`)
	p := parseFile(raw)
	if p.from != "" {
		t.Errorf("unclosed frontmatter should not parse; got from=%q", p.from)
	}
	if !strings.HasPrefix(p.body, "---") {
		t.Errorf("body should preserve the original text")
	}
}

func TestParseFile_BlockScalarPipe(t *testing.T) {
	raw := []byte(`---
subject: |
  Multi-line
  subject value
---
body
`)
	p := parseFile(raw)
	if p.subject != "Multi-line\nsubject value" {
		t.Errorf("subject: %q", p.subject)
	}
}

func TestParseFile_CommentsAndBlanksIgnored(t *testing.T) {
	raw := []byte(`---
# this is a comment
from: ok@example.com

# blank-line above and this comment-only line are skipped
to: dest@example.com
---
body
`)
	p := parseFile(raw)
	if p.from != "ok@example.com" || p.to != "dest@example.com" {
		t.Errorf("from=%q to=%q", p.from, p.to)
	}
}

func TestParseDate_AcceptsCommonFormats(t *testing.T) {
	for _, in := range []string{
		"2026-05-20T10:00:00Z",
		"2026-05-20T10:00:00.123Z",
		"2026-05-20 10:00:00",
		"2026-05-20",
	} {
		got := parseDate(in)
		if got.IsZero() {
			t.Errorf("expected to parse %q", in)
		}
	}
}

func TestParseDate_BadInputReturnsZero(t *testing.T) {
	if !parseDate("not-a-date").IsZero() {
		t.Error("expected zero time for bad input")
	}
	if !parseDate("").IsZero() {
		t.Error("expected zero time for empty input")
	}
}

func TestSplitFrontmatter_StripsBOM(t *testing.T) {
	raw := "\ufeff---\nfrom: x\n---\nbody\n"
	fm, body, ok := splitFrontmatter(raw)
	if !ok {
		t.Fatal("expected frontmatter to be detected past BOM")
	}
	if !strings.Contains(fm, "from: x") {
		t.Errorf("fm: %q", fm)
	}
	if body != "body\n" {
		t.Errorf("body: %q", body)
	}
}
