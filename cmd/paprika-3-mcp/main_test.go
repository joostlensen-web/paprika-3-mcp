package main

import (
	"context"
	"runtime"
	"testing"
)

func TestResolvePassword(t *testing.T) {
	var asked string
	readKeychain = func(ctx context.Context, service string) (string, error) {
		asked = service
		return "from-keychain", nil
	}

	t.Run("flag when nothing else is set", func(t *testing.T) {
		t.Setenv("PAPRIKA_PASSWORD", "")
		if got, _ := resolvePassword("", "from-flag"); got != "from-flag" {
			t.Errorf("got %q, want from-flag", got)
		}
	})

	t.Run("environment wins over flag", func(t *testing.T) {
		t.Setenv("PAPRIKA_PASSWORD", "from-env")
		if got, _ := resolvePassword("", "from-flag"); got != "from-env" {
			t.Errorf("got %q, want from-env", got)
		}
	})

	t.Run("keychain wins over environment and flag", func(t *testing.T) {
		if runtime.GOOS != "darwin" {
			t.Skip("keychain is macOS only")
		}
		t.Setenv("PAPRIKA_PASSWORD", "from-env")
		got, err := resolvePassword("paprika-mcp", "from-flag")
		if err != nil || got != "from-keychain" {
			t.Errorf("got %q, %v; want from-keychain", got, err)
		}
		if asked != "paprika-mcp" {
			t.Errorf("asked keychain for %q, want paprika-mcp", asked)
		}
	})
}
