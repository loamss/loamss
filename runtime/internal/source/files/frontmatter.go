package files

import (
	"bufio"
	"strings"
	"time"
)

// parsedFile is the result of running parseFile on a raw file's
// bytes. Fields are populated from YAML frontmatter where present;
// the body field is the file contents minus any frontmatter block.
type parsedFile struct {
	// Email-shaped headers — feed directly into the memory layer's
	// entity resolver.
	from string
	to   string
	cc   string
	bcc  string

	// Conversational metadata.
	subject string
	thread  string
	date    time.Time

	// Fallback identity.
	title string

	// Any frontmatter key we didn't recognize, preserved so capsules
	// can use it without a code change to the source.
	extras map[string]string

	// File body, minus the frontmatter block.
	body string
}

// parseFile reads a file's bytes and extracts YAML frontmatter if
// present. Frontmatter is the leading block delimited by "---" on
// its own line at the start of the file and a closing "---" line.
//
// Why not depend on a YAML library? The parser here handles the
// subset of YAML we actually expect from human-written frontmatter:
//
//	key: value
//	key: "quoted value with: colons"
//	key: |
//	  multiline
//	  value
//
// Lists and nested objects aren't supported — if a connector ever
// needs them, that's the moment to pull in yaml.v3 as a dep. Until
// then, every byte of dependency we don't add is a byte the binary
// doesn't carry.
func parseFile(raw []byte) parsedFile {
	out := parsedFile{extras: map[string]string{}}
	text := string(raw)
	frontmatter, body, ok := splitFrontmatter(text)
	if !ok {
		out.body = text
		return out
	}
	out.body = body

	for _, line := range parseFrontmatterLines(frontmatter) {
		key, value := line.key, line.value
		switch strings.ToLower(key) {
		case "from":
			out.from = value
		case "to":
			out.to = value
		case "cc":
			out.cc = value
		case "bcc":
			out.bcc = value
		case "subject":
			out.subject = value
		case "title":
			out.title = value
		case "thread", "thread_id":
			out.thread = value
		case "date":
			out.date = parseDate(value)
		default:
			if value != "" {
				out.extras[key] = value
			}
		}
	}
	return out
}

// splitFrontmatter detects a YAML frontmatter block at the start of
// text and returns (frontmatterBody, remainingBody, hadFrontmatter).
// A leading BOM and any leading blank lines are tolerated.
func splitFrontmatter(text string) (string, string, bool) {
	// Strip UTF-8 BOM (U+FEFF) if present.
	text = strings.TrimPrefix(text, "\ufeff")
	// Detect the opening delimiter, allowing a leading whitespace-
	// only line (rare but happens).
	lines := strings.SplitN(text, "\n", 3)
	if len(lines) < 1 {
		return "", text, false
	}
	first := strings.TrimRight(lines[0], "\r")
	if strings.TrimSpace(first) != "---" {
		return "", text, false
	}
	// Find the closing delimiter.
	rest := text[len(lines[0])+1:]
	closeIdx := findClosingDelimiter(rest)
	if closeIdx < 0 {
		// Unclosed frontmatter — treat as no frontmatter so we don't
		// silently swallow the whole file.
		return "", text, false
	}
	body := rest[closeIdx:]
	// Skip past the closing "---" + newline.
	bodyLines := strings.SplitN(body, "\n", 2)
	if len(bodyLines) == 2 {
		body = bodyLines[1]
	} else {
		body = ""
	}
	return rest[:closeIdx], strings.TrimLeft(body, "\r\n"), true
}

// findClosingDelimiter returns the byte index of a line containing
// only "---" within s, searching line by line. Returns -1 if not
// found.
func findClosingDelimiter(s string) int {
	scanner := bufio.NewScanner(strings.NewReader(s))
	scanner.Buffer(make([]byte, 0, 1024), 1024*1024)
	offset := 0
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if strings.TrimSpace(line) == "---" {
			return offset
		}
		offset += len(scanner.Bytes()) + 1 // +1 for the newline
	}
	return -1
}

type frontmatterLine struct {
	key   string
	value string
}

// parseFrontmatterLines returns the key/value pairs from a
// frontmatter block. Multi-line values introduced by `|` or `>` are
// joined into a single string (newlines preserved for `|`, replaced
// with spaces for `>`).
func parseFrontmatterLines(text string) []frontmatterLine {
	var out []frontmatterLine
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		raw := strings.TrimRight(lines[i], "\r")
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		colonIdx := strings.IndexByte(raw, ':')
		if colonIdx <= 0 {
			continue
		}
		key := strings.TrimSpace(raw[:colonIdx])
		value := strings.TrimSpace(raw[colonIdx+1:])

		// Block scalar — gather following indented lines.
		if value == "|" || value == ">" {
			joiner := "\n"
			if value == ">" {
				joiner = " "
			}
			var parts []string
			j := i + 1
			for ; j < len(lines); j++ {
				next := lines[j]
				if next == "" {
					parts = append(parts, "")
					continue
				}
				if !strings.HasPrefix(next, " ") && !strings.HasPrefix(next, "\t") {
					break
				}
				parts = append(parts, strings.TrimLeft(next, " \t"))
			}
			// Trim trailing empty lines so blank lines between the
			// block scalar and the next key (or the closing "---")
			// don't sneak into the joined value.
			for len(parts) > 0 && parts[len(parts)-1] == "" {
				parts = parts[:len(parts)-1]
			}
			value = strings.Join(parts, joiner)
			i = j - 1
		} else {
			value = unquote(value)
		}
		out = append(out, frontmatterLine{key: key, value: value})
	}
	return out
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// parseDate accepts common date formats found in human-written
// frontmatter. Returns the zero time.Time on failure rather than
// erroring out — a bad date should not abort ingestion.
func parseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
		"02 Jan 2006",
		"January 2, 2006",
	}
	for _, layout := range formats {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
