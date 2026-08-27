// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"context"
	"reflect"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/regtest"
)

// #529-style scoping for regtest: resolveRegtestProjectKeys must return
// every source project key that fully matches --project_key as an
// anchored (^...$) regex, mirroring resolveTransferProjectKeys so a
// project-scoped transfer can be verified without regtest comparing
// every other, deliberately un-migrated source project.
func TestResolveRegtestProjectKeys_PatternMatchesAnchored(t *testing.T) {
	srv := newProjectListingMockServer(t, []string{
		"BANKING_core", "BANKING_payments", "other-project", "not-BANKING_at-all",
	})

	cfg := regtest.Config{SQSURL: srv.URL, SQSToken: "tok"}
	got, err := resolveRegtestProjectKeys(context.Background(), cfg, "BANKING_.+")
	if err != nil {
		t.Fatalf("resolveRegtestProjectKeys: %v", err)
	}
	want := []string{"BANKING_core", "BANKING_payments"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (sorted, anchored match only)", got, want)
	}
}

func TestResolveRegtestProjectKeys_LiteralKeyMatchesItself(t *testing.T) {
	srv := newProjectListingMockServer(t, []string{"my-project", "my-project-2", "other"})

	cfg := regtest.Config{SQSURL: srv.URL, SQSToken: "tok"}
	got, err := resolveRegtestProjectKeys(context.Background(), cfg, "my-project")
	if err != nil {
		t.Fatalf("resolveRegtestProjectKeys: %v", err)
	}
	if want := []string{"my-project"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestResolveRegtestProjectKeys_NoMatchIsAnError(t *testing.T) {
	srv := newProjectListingMockServer(t, []string{"other-project"})

	cfg := regtest.Config{SQSURL: srv.URL, SQSToken: "tok"}
	_, err := resolveRegtestProjectKeys(context.Background(), cfg, "BANKING_.+")
	if err == nil {
		t.Fatal("expected an error when the pattern matches no source project")
	}
	if !contains(err.Error(), "BANKING_.+") {
		t.Errorf("error %q should name the pattern", err.Error())
	}
}
