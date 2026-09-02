// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/scanreport"
	pb "github.com/sonar-solutions/sonar-migration-tool/internal/scanreport/proto"
)

func TestResolveMigrateHistory(t *testing.T) {
	set := func(v bool) *FlexibleBool { return &FlexibleBool{Set: true, Value: v} }

	cases := []struct {
		name       string
		target     *FlexibleBool
		top        *FlexibleBool
		wantResult bool
	}{
		{"neither set defaults false", nil, nil, false},
		{"top-level true, no target", nil, set(true), true},
		{"target true wins over unset top-level", set(true), nil, true},
		{"target false wins over top-level true", set(false), set(true), false},
		{"target unset falls back to top-level true", &FlexibleBool{Set: false}, set(true), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveMigrateHistory(c.target, c.top)
			if got != c.wantResult {
				t.Errorf("resolveMigrateHistory(%v, %v) = %v, want %v", c.target, c.top, got, c.wantResult)
			}
		})
	}
}

func TestExtractHistoryMeasures(t *testing.T) {
	data := json.RawMessage(`{
		"projectKey": "proj",
		"branch": "main",
		"date": "2024-05-14T10:45:33Z",
		"measures": [
			{"metric": "ncloc", "value": "1000"},
			{"metric": "bugs", "value": "3"},
			{"metric": "empty_value", "value": ""},
			{"value": "no-metric-key"}
		]
	}`)

	got := extractHistoryMeasures(data, "my_cloud_key")
	if len(got) != 2 {
		t.Fatalf("expected 2 valid measures, got %d: %+v", len(got), got)
	}
	byMetric := map[string]string{}
	for _, m := range got {
		if m.Component != "my_cloud_key" {
			t.Errorf("expected component %q, got %q", "my_cloud_key", m.Component)
		}
		byMetric[m.MetricKey] = m.Value
	}
	if byMetric["ncloc"] != "1000" || byMetric["bugs"] != "3" {
		t.Errorf("unexpected measures map: %v", byMetric)
	}
}

func TestExtractHistoryMeasuresNoMeasuresField(t *testing.T) {
	data := json.RawMessage(`{"projectKey": "proj"}`)
	if got := extractHistoryMeasures(data, "proj"); got != nil {
		t.Errorf("expected nil for a record with no measures field, got %v", got)
	}
}

// TestHistoricalReportShape builds the same protobuf pieces
// submitHistoricalSnapshot assembles (without actually submitting over the
// network — that's covered by the live end-to-end verification) and asserts
// its shape: one placeholder file component carrying the measures, and a
// metadata analysis date matching the snapshot's backdated date rather than
// time.Now().
func TestHistoricalReportShape(t *testing.T) {
	snap := historySnapshot{
		Date:           time.Date(2022, 3, 1, 0, 0, 0, 0, time.UTC),
		ProjectVersion: "2.5",
		Measures: []scanreport.MeasureInput{
			{Component: "cloud_key", MetricKey: "ncloc", Value: "500"},
		},
	}
	placeholder := historyPlaceholder{
		Language: "js",
		Ext:      "js",
		QProfile: scanreport.QProfileInfo{Key: "target-org-js-key", Name: "Sonar way", Language: "js"},
	}

	placeholderName := "__history_snapshot__." + placeholder.Ext
	placeholderKey := "cloud_key:" + placeholderName
	root, fileComps, cr := scanreport.BuildComponents("cloud_key", []scanreport.ComponentInput{
		{Key: placeholderKey, Name: placeholderName, Path: placeholderName, Language: placeholder.Language, Lines: 1},
	})
	if len(fileComps) != 1 {
		t.Fatalf("expected exactly 1 placeholder file component, got %d", len(fileComps))
	}
	if root.GetType() != pb.Component_PROJECT {
		t.Errorf("expected root component type PROJECT, got %v", root.GetType())
	}

	md := scanreport.BuildMetadata(scanreport.MetadataInput{
		AnalysisDate:   snap.Date,
		ProjectKey:     "cloud_key",
		BranchName:     "main",
		BranchType:     pb.Metadata_BRANCH,
		QProfiles:      []scanreport.QProfileInfo{placeholder.QProfile},
		FileCountByExt: map[string]int32{placeholder.Language: 1},
		ProjectVersion: snap.ProjectVersion,
	}, root.Ref)
	if md.AnalysisDate != snap.Date.UnixMilli() {
		t.Errorf("expected metadata analysis date %d (the backdated snapshot date), got %d",
			snap.Date.UnixMilli(), md.AnalysisDate)
	}
	if md.AnalysisUuid != "" {
		t.Errorf("expected no analysis UUID for a main-branch history point, got %q", md.AnalysisUuid)
	}
	// Regression guard for the bug this PoC shipped with first: the report
	// carried a quality profile key hardcoded from the developer's own
	// organization, so the Compute Engine rejected every historical report
	// with "Quality profiles with following keys don't exist in organization".
	// The key in the metadata must be the one resolved from the TARGET org.
	gotProfile, ok := md.QprofilesPerLanguage[placeholder.Language]
	if !ok {
		t.Fatalf("expected a qprofile entry for language %q, got %v", placeholder.Language, md.QprofilesPerLanguage)
	}
	if gotProfile.GetKey() != placeholder.QProfile.Key {
		t.Errorf("expected the target organization's profile key %q in metadata, got %q",
			placeholder.QProfile.Key, gotProfile.GetKey())
	}

	measures := scanreport.BuildMeasures(retargetMeasures(snap.Measures, placeholderKey), cr)
	if len(measures[fileComps[0].Ref]) != 1 {
		t.Fatalf("expected 1 measure on the placeholder file component ref, got %d", len(measures[fileComps[0].Ref]))
	}
}

// TestResolveHistoryPlaceholderProfilePrefersCommonLanguage pins the
// placeholder-language choice: it must come from the target organization's
// own profile map, never from a constant.
func TestResolveHistoryPlaceholderProfilePrefersCommonLanguage(t *testing.T) {
	tests := []struct {
		name     string
		byLang   map[string]scanreport.QProfileInfo
		wantLang string
		wantOK   bool
	}{
		{
			name:   "no profiles in org means history cannot be migrated",
			byLang: map[string]scanreport.QProfileInfo{},
			wantOK: false,
		},
		{
			name: "prefers js when available",
			byLang: map[string]scanreport.QProfileInfo{
				"js":   {Key: "k-js", Language: "js"},
				"abap": {Key: "k-abap", Language: "abap"},
			},
			wantLang: "js", wantOK: true,
		},
		{
			name: "falls back to the next preferred language",
			byLang: map[string]scanreport.QProfileInfo{
				"abap": {Key: "k-abap", Language: "abap"},
				"py":   {Key: "k-py", Language: "py"},
			},
			wantLang: "py", wantOK: true,
		},
		{
			// Refusing is deliberate. A language key is not reliably its own
			// file extension (apex->.cls, cobol->.cbl, web->.html), and Cloud
			// derives a file's language from its extension — guessing one
			// recreates the #474 whole-report rejection. Skipping history
			// beats submitting reports the CE will refuse.
			name: "refuses when the org has profiles but none we can name a file for",
			byLang: map[string]scanreport.QProfileInfo{
				"cobol": {Key: "k-cobol", Language: "cobol"},
				"abap":  {Key: "k-abap", Language: "abap"},
			},
			wantOK: false,
		},
		{
			// Every supported placeholder language must carry an extension
			// the target maps back to that same language.
			name: "picks a supported language even when unsupported ones sort first",
			byLang: map[string]scanreport.QProfileInfo{
				"abap": {Key: "k-abap", Language: "abap"},
				"xml":  {Key: "k-xml", Language: "xml"},
			},
			wantLang: "xml", wantOK: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pickHistoryPlaceholder(tc.byLang)
			if ok != tc.wantOK {
				t.Fatalf("ok: got %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if got.Language != tc.wantLang {
				t.Errorf("language: got %q, want %q", got.Language, tc.wantLang)
			}
			if got.QProfile.Key != tc.byLang[tc.wantLang].Key {
				t.Errorf("profile key: got %q, want %q", got.QProfile.Key, tc.byLang[tc.wantLang].Key)
			}
		})
	}
}
