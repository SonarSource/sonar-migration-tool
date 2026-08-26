// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package wizard

import (
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

const (
	testServerURL     = "https://sonar.example.com"
	testServerURLSlash = "https://sonar.example.com/"
	// testNonLocalIP is a non-localhost IP used to verify isLocalhostURL returns false for non-local addresses.
	testNonLocalIP = "https://10.0.0.1:9000" //NOSONAR — intentional non-local IP for testing isLocalhostURL
)

func TestValidateServerURL(t *testing.T) {
	tests := []struct {
		url     string
		wantErr bool
	}{
		{testServerURL, false},
		{"http://sonar.example.com", false},
		{testServerURLSlash, false},
		{"https://sonar.example.com:9000", false},
		{"ftp://sonar.example.com", true},
		{"sonar.example.com", true},
		{"", true},
		{"https://", true},
	}

	for _, tt := range tests {
		err := validateServerURL(tt.url)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateServerURL(%q) error=%v, wantErr=%v", tt.url, err, tt.wantErr)
		}
	}
}

func TestIsLocalhostURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"http://localhost:9000", true},
		{"https://127.0.0.1:9000", true},     //nolint:gosec
		{"http://[::1]:9000", true},
		{testServerURL, false},
		{testNonLocalIP, false},
		{"not-a-url", false},
	}

	for _, tt := range tests {
		got := isLocalhostURL(tt.url)
		if got != tt.want {
			t.Errorf("isLocalhostURL(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestNormalizeTrailingSlash(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{testServerURL, testServerURLSlash},
		{testServerURLSlash, testServerURLSlash},
		{"", ""},
	}

	for _, tt := range tests {
		got := normalizeTrailingSlash(tt.input)
		if got != tt.want {
			t.Errorf("normalizeTrailingSlash(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIsSSLError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"generic", fmt.Errorf("connection refused"), false},
		{"x509 unknown authority", x509.UnknownAuthorityError{}, true},
		{"wrapped x509", fmt.Errorf("connect: %w", x509.UnknownAuthorityError{}), true},
		{"string match", fmt.Errorf("x509: certificate signed by unknown authority"), true},
		{"cert string", fmt.Errorf("tls: certificate required"), true},
	}

	for _, tt := range tests {
		got := isSSLError(tt.err)
		if got != tt.want {
			t.Errorf("isSSLError(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestCheckRequiredFiles(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"a.csv", "b.csv"} {
		os.WriteFile(filepath.Join(dir, name), []byte("data"), 0o644)
	}

	missing := checkRequiredFiles(dir, []string{"a.csv", "b.csv", "c.csv", "d.csv"})
	if len(missing) != 2 {
		t.Fatalf("expected 2 missing, got %d: %v", len(missing), missing)
	}
	if missing[0] != "c.csv" || missing[1] != "d.csv" {
		t.Errorf("expected [c.csv d.csv], got %v", missing)
	}

	missing = checkRequiredFiles(dir, []string{"a.csv", "b.csv"})
	if len(missing) != 0 {
		t.Errorf("expected 0 missing, got %v", missing)
	}
}

func TestOrgsFromMaps(t *testing.T) {
	rows := []map[string]any{
		{
			"sonarqube_org_key":  "org-1",
			"sonarcloud_org_key": "cloud-1",
			"binding_key":        "binding-1",
			"server_url":         testServerURLSlash,
			"alm":                "github",
			"url":                "https://github.com/org",
			"is_cloud":           true,
			"project_count":      float64(5),
		},
		{
			"sonarqube_org_key":  "org-2",
			"sonarcloud_org_key": SkippedOrgSentinel,
			"binding_key":        "",
			"server_url":         "https://sq2.example.com/",
			"alm":                "",
			"url":                "",
			"is_cloud":           false,
			"project_count":      float64(0),
		},
	}

	orgs := orgsFromMaps(rows)
	if len(orgs) != 2 {
		t.Fatalf("expected 2 orgs, got %d", len(orgs))
	}

	assertOrg(t, orgs[0], structure.Organization{
		SonarQubeOrgKey:  "org-1",
		SonarCloudOrgKey: "cloud-1",
		BindingKey:       "binding-1",
		ServerURL:        testServerURLSlash,
		ALM:              "github",
		URL:              "https://github.com/org",
		IsCloud:          true,
		ProjectCount:     5,
	})

	if orgs[1].SonarCloudOrgKey != SkippedOrgSentinel {
		t.Errorf("expected SKIPPED, got %q", orgs[1].SonarCloudOrgKey)
	}
}

func assertOrg(t *testing.T, got, want structure.Organization) {
	t.Helper()
	if got.SonarQubeOrgKey != want.SonarQubeOrgKey {
		t.Errorf("SonarQubeOrgKey: %q != %q", got.SonarQubeOrgKey, want.SonarQubeOrgKey)
	}
	if got.SonarCloudOrgKey != want.SonarCloudOrgKey {
		t.Errorf("SonarCloudOrgKey: %q != %q", got.SonarCloudOrgKey, want.SonarCloudOrgKey)
	}
	if got.BindingKey != want.BindingKey {
		t.Errorf("BindingKey: %q != %q", got.BindingKey, want.BindingKey)
	}
	if got.ServerURL != want.ServerURL {
		t.Errorf("ServerURL: %q != %q", got.ServerURL, want.ServerURL)
	}
	if got.ALM != want.ALM {
		t.Errorf("ALM: %q != %q", got.ALM, want.ALM)
	}
	if got.IsCloud != want.IsCloud {
		t.Errorf("IsCloud: %v != %v", got.IsCloud, want.IsCloud)
	}
	if got.ProjectCount != want.ProjectCount {
		t.Errorf("ProjectCount: %d != %d", got.ProjectCount, want.ProjectCount)
	}
}

func TestMapStrMissing(t *testing.T) {
	m := map[string]any{"key": 123} // not a string
	if mapStr(m, "key") != "" {
		t.Error("expected empty for non-string value")
	}
	if mapStr(m, "missing") != "" {
		t.Error("expected empty for missing key")
	}
}

func TestMapBoolMissing(t *testing.T) {
	m := map[string]any{"key": "not-bool"}
	if mapBool(m, "key") != false {
		t.Error("expected false for non-bool value")
	}
	if mapBool(m, "missing") != false {
		t.Error("expected false for missing key")
	}
}

func TestMapIntVariants(t *testing.T) {
	m := map[string]any{
		"float": float64(42),
		"int":   7,
		"str":   "nope",
	}
	if mapInt(m, "float") != 42 {
		t.Errorf("float: got %d", mapInt(m, "float"))
	}
	if mapInt(m, "int") != 7 {
		t.Errorf("int: got %d", mapInt(m, "int"))
	}
	if mapInt(m, "str") != 0 {
		t.Error("expected 0 for string value")
	}
	if mapInt(m, "missing") != 0 {
		t.Error("expected 0 for missing key")
	}
}

func TestPhaseSequence(t *testing.T) {
	if PhaseCount() != 6 {
		t.Errorf("expected 6 phases, got %d", PhaseCount())
	}
	if PhaseIndex(PhaseExtract) != 1 {
		t.Errorf("PhaseExtract should be index 1, got %d", PhaseIndex(PhaseExtract))
	}
	if PhaseIndex(PhaseMigrate) != 6 {
		t.Errorf("PhaseMigrate should be index 6, got %d", PhaseIndex(PhaseMigrate))
	}
	if PhaseIndex(PhaseInit) != 0 {
		t.Errorf("PhaseInit should not be in sequence, got index %d", PhaseIndex(PhaseInit))
	}
}

func TestNextPhase(t *testing.T) {
	tests := []struct {
		current WizardPhase
		want    WizardPhase
	}{
		{PhaseExtract, PhaseStructure},
		{PhaseStructure, PhaseOrgMapping},
		{PhaseMigrate, PhaseComplete},
		{PhaseComplete, PhaseComplete},
	}

	for _, tt := range tests {
		got := nextPhase(tt.current)
		if got != tt.want {
			t.Errorf("nextPhase(%q) = %q, want %q", tt.current, got, tt.want)
		}
	}
}

func TestPhasesUpTo(t *testing.T) {
	names := phasesUpTo(PhaseOrgMapping)
	want := []string{"Extract", "Structure", "Organization Mapping"}
	if len(names) != len(want) {
		t.Fatalf("expected %d names, got %d: %v", len(want), len(names), names)
	}
	for i, n := range names {
		if n != want[i] {
			t.Errorf("index %d: got %q, want %q", i, n, want[i])
		}
	}
}

func TestPhasesUpToAll(t *testing.T) {
	names := phasesUpTo(PhaseMigrate)
	if len(names) != 6 {
		t.Errorf("expected 6 phases, got %d", len(names))
	}
}

func TestPhaseByIndex(t *testing.T) {
	tests := []struct {
		idx  int
		want WizardPhase
	}{
		{0, PhaseExtract},
		{2, PhaseOrgMapping},
		{5, PhaseMigrate},
		{-1, PhaseExtract},
		{99, PhaseExtract},
	}
	for _, tt := range tests {
		got := phaseByIndex(tt.idx)
		if got != tt.want {
			t.Errorf("phaseByIndex(%d) = %q, want %q", tt.idx, got, tt.want)
		}
	}
}

func TestStrPtr(t *testing.T) {
	p := strPtr("hello")
	if *p != "hello" {
		t.Errorf("strPtr: got %q", *p)
	}
}

func TestPtrStr(t *testing.T) {
	if ptrStr(nil) != "" {
		t.Error("ptrStr(nil) should be empty")
	}
	s := "hello"
	if ptrStr(&s) != "hello" {
		t.Errorf("ptrStr: got %q", ptrStr(&s))
	}
}

func TestGenerateRunID(t *testing.T) {
	dir := t.TempDir()
	id := generateRunID(dir)
	if id == "" {
		t.Error("generateRunID should return non-empty string")
	}
	if id[len(id)-5:] != "-0001" {
		t.Errorf("expected -0001 suffix, got %q", id)
	}
	// Issue #108 — the date prefix is ISO (YYYY-MM-DD), not the
	// previous US format (MM-DD-YYYY). The ISO format starts with
	// the four-digit year and sorts chronologically. We don't
	// hard-code today's date (test would drift) but pin the shape:
	// id must be at least YYYY-MM-DD-NNNN = 15 chars (#542 — the
	// sequence number is zero-padded to four digits so lexicographic
	// and numeric ordering agree up to 9999 runs/day), must start
	// with today's UTC year, and the 5th character must be a hyphen.
	want := time.Now().UTC().Format("2006")
	if len(id) < 15 {
		t.Fatalf("id too short for ISO format YYYY-MM-DD-NNNN: %q", id)
	}
	if id[:4] != want {
		t.Errorf("id must start with current UTC year %q, got %q (full id %q)", want, id[:4], id)
	}
	if id[4] != '-' {
		t.Errorf("id[4] must be '-' for ISO format, got %q (full id %q)", string(id[4]), id)
	}
}

func TestPhaseDisplayName(t *testing.T) {
	tests := []struct {
		phase WizardPhase
		want  string
	}{
		{PhaseExtract, "Extract"},
		{PhaseOrgMapping, "Organization Mapping"},
		{WizardPhase("unknown"), "unknown"},
	}
	for _, tt := range tests {
		got := PhaseDisplayName(tt.phase)
		if got != tt.want {
			t.Errorf("PhaseDisplayName(%q) = %q, want %q", tt.phase, got, tt.want)
		}
	}
}
