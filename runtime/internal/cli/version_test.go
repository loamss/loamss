package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"version"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("version command failed: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"loamss ",
		"commit:",
		"built:",
		"go version:",
		"platform:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("version output missing %q\ngot:\n%s", want, out)
		}
	}
}

func TestRootCommandHelp(t *testing.T) {
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"--help"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("--help failed: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"personal data infrastructure", // from the Long
		"Usage:",
		"Available Commands:",
		"version",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("root help missing %q\ngot:\n%s", want, out)
		}
	}
}
