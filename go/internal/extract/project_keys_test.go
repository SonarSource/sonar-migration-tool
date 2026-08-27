// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"testing"
)

func TestCompileProjectKeyPatternAnchoring(t *testing.T) {
	re, err := CompileProjectKeyPattern("BANKING_.+")
	if err != nil {
		t.Fatalf("CompileProjectKeyPattern: %v", err)
	}
	if !re.MatchString("BANKING_portal") {
		t.Error("expected BANKING_portal to match")
	}
	if re.MatchString("other_BANKING_portal") {
		t.Error("pattern must be anchored — should not match a substring occurrence")
	}

	// A literal key with no regex metacharacters must match only itself.
	litRe, err := CompileProjectKeyPattern("my-project")
	if err != nil {
		t.Fatalf("CompileProjectKeyPattern: %v", err)
	}
	if !litRe.MatchString("my-project") {
		t.Error("expected exact literal match")
	}
	if litRe.MatchString("my-project-2") {
		t.Error("literal pattern should not match a superstring")
	}

	if _, err := CompileProjectKeyPattern("("); err == nil {
		t.Error("expected error for invalid regex")
	}
}

func TestResolveProjectKeysMatchesSubset(t *testing.T) {
	srv := newMockServer() // serves proj1, proj2 (see newMockServer)
	defer srv.Close()

	keys, err := ResolveProjectKeys(context.Background(), ExtractConfig{URL: srv.URL, Token: testToken}, "proj1")
	if err != nil {
		t.Fatalf("ResolveProjectKeys: %v", err)
	}
	if len(keys) != 1 || keys[0] != "proj1" {
		t.Errorf("keys = %v, want [proj1]", keys)
	}
}

func TestResolveProjectKeysMatchesAllViaRegex(t *testing.T) {
	srv := newMockServer()
	defer srv.Close()

	keys, err := ResolveProjectKeys(context.Background(), ExtractConfig{URL: srv.URL, Token: testToken}, "proj.*")
	if err != nil {
		t.Fatalf("ResolveProjectKeys: %v", err)
	}
	want := []string{"proj1", "proj2"}
	if len(keys) != len(want) || keys[0] != want[0] || keys[1] != want[1] {
		t.Errorf("keys = %v, want %v", keys, want)
	}
}

func TestResolveProjectKeysZeroMatchesErrors(t *testing.T) {
	srv := newMockServer()
	defer srv.Close()

	_, err := ResolveProjectKeys(context.Background(), ExtractConfig{URL: srv.URL, Token: testToken}, "no-such-project")
	if err == nil {
		t.Fatal("expected error when no project matches the pattern")
	}
}

func TestResolveProjectKeysInvalidPatternErrors(t *testing.T) {
	srv := newMockServer()
	defer srv.Close()

	_, err := ResolveProjectKeys(context.Background(), ExtractConfig{URL: srv.URL, Token: testToken}, "(")
	if err == nil {
		t.Fatal("expected error for invalid regex pattern")
	}
}
