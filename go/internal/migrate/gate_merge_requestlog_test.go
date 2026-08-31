// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqapi "github.com/sonar-solutions/sq-api-go"
)

// gates.csv is deduplicated on the SOURCE org key, but the load then joins
// source org to CLOUD org many-to-one, so N source orgs sharing a gate name
// yield N records carrying the same cloud_gate_id. Fanned out concurrently
// each ran clear-then-recreate against the same gate: one worker won every
// race and the rest logged 404s on delete and "already exists" on create.
// Merging first makes the outcome deterministic.
func TestMergeGateRecordsByCloudGate(t *testing.T) {
	rec := func(org, gateID, name string, preexisting bool, metrics ...string) json.RawMessage {
		conds := make([]map[string]any, 0, len(metrics))
		for _, m := range metrics {
			conds = append(conds, map[string]any{"metric": m, "op": "GT", "error": "1"})
		}
		b, _ := json.Marshal(map[string]any{
			"sonarcloud_org_key": org,
			"cloud_gate_id":      gateID,
			"gate_name":          name,
			"was_preexisting":    preexisting,
			"conditions":         conds,
		})
		return b
	}

	in := []json.RawMessage{
		rec("org1", "49476", "Corp base", false, "new_coverage"),
		rec("org1", "49476", "Corp base", true, "new_bugs", "new_coverage"),
		rec("org1", "49477", "Other gate", false, "new_smells"),
		rec("org2", "49476", "Corp base", false, "new_debt"), // same id, different org
	}

	out := mergeGateRecordsByCloudGate(in)

	if len(out) != 3 {
		t.Fatalf("expected 3 merged records (org1/49476, org1/49477, org2/49476), got %d", len(out))
	}

	byKey := map[string]json.RawMessage{}
	for _, r := range out {
		byKey[extractField(r, "sonarcloud_org_key")+"/"+extractField(r, "cloud_gate_id")] = r
	}

	merged, ok := byKey["org1/49476"]
	if !ok {
		t.Fatal("merged record for org1/49476 missing")
	}
	// Conditions are the union of both contributing records.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(merged, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var conds []map[string]any
	if err := json.Unmarshal(obj["conditions"], &conds); err != nil {
		t.Fatalf("conditions: %v", err)
	}
	if len(conds) != 3 {
		t.Errorf("expected the union of 1+2 conditions, got %d", len(conds))
	}
	// was_preexisting must be OR-ed so the clear still runs.
	if !extractBool(merged, "was_preexisting") {
		t.Error("was_preexisting must be true when any contributing record saw an existing gate")
	}
	// A different org sharing the numeric id must stay separate.
	if _, ok := byKey["org2/49476"]; !ok {
		t.Error("records must be keyed by (org, gate id), not gate id alone")
	}
}

// A record with no cloud_gate_id cannot be keyed; it must survive to the
// task body, which skips it, rather than being silently dropped here.
func TestMergeGateRecordsByCloudGatePassesThroughUnkeyable(t *testing.T) {
	b, _ := json.Marshal(map[string]any{"gate_name": "no id", "conditions": []any{}})
	out := mergeGateRecordsByCloudGate([]json.RawMessage{b})
	if len(out) != 1 {
		t.Fatalf("expected the unkeyable record to pass through, got %d", len(out))
	}
}

// requests.log had seven readers in the report pipeline and no producer, so
// every failure they were meant to surface came out empty. The format is
// fixed by those parsers, so assert it precisely.
func TestRequestLogWriterFormat(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))

	w := newRequestLogWriter()
	// Buffered before the run directory exists...
	w.Log(sqapi.RequestLogEntry{Method: "GET", URL: "/api/organizations/search", Status: 200})
	w.Open(dir, logger)
	// ...and streamed after.
	w.Log(sqapi.RequestLogEntry{
		Method:   "POST",
		URL:      "/api/settings/set",
		Status:   400,
		Data:     map[string]string{"key": "sonar.dbcleaner.x", "component": "org1_proj1"},
		Response: `{"errors":[{"msg":"Setting 'sonar.dbcleaner.x' cannot be set on a Project"}]}`,
	})
	w.Close()

	f, err := os.Open(filepath.Join(dir, "requests.log"))
	if err != nil {
		t.Fatalf("requests.log was not created: %v", err)
	}
	defer f.Close()

	var entries []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line is not valid JSON: %v (%s)", err, sc.Text())
		}
		entries = append(entries, m)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (1 buffered + 1 streamed), got %d", len(entries))
	}

	// Every reader gates on this exact process_type.
	for i, e := range entries {
		if e["process_type"] != "request_completed" {
			t.Errorf("entry %d: process_type = %v, want request_completed", i, e["process_type"])
		}
	}
	if entries[0]["status"] != "success" {
		t.Errorf("200 must be status=success, got %v", entries[0]["status"])
	}
	if entries[1]["status"] != "failure" {
		t.Errorf("400 must be status=failure, got %v", entries[1]["status"])
	}

	payload, ok := entries[1]["payload"].(map[string]any)
	if !ok {
		t.Fatal("payload missing on the failure entry")
	}
	if payload["url"] != "/api/settings/set" || payload["method"] != "POST" {
		t.Errorf("payload method/url wrong: %+v", payload)
	}
	if payload["status"] != float64(400) {
		t.Errorf("payload status = %v, want 400", payload["status"])
	}
	data, ok := payload["data"].(map[string]any)
	if !ok || data["key"] != "sonar.dbcleaner.x" {
		t.Errorf("form data not recorded: %+v", payload["data"])
	}
	if s, _ := payload["response"].(string); !strings.Contains(s, "cannot be set on a Project") {
		t.Errorf("error body not recorded: %v", payload["response"])
	}
}

// requests.log lands in the run directory in plaintext and is routinely
// attached to support tickets, so secured setting values must never reach it.
func TestRequestLogRedactsSecuredValues(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	w := newRequestLogWriter()
	w.Open(dir, logger)
	w.Log(sqapi.RequestLogEntry{
		Method: "POST", URL: "/api/settings/set", Status: 400,
		// redactFormValue has already run in the transport; assert the
		// writer persists the redaction rather than the secret.
		Data: map[string]string{"key": "sonar.auth.github.clientSecret.secured", "value": "<redacted>"},
	})
	w.Close()

	b, err := os.ReadFile(filepath.Join(dir, "requests.log"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Decode rather than substring-match: encoding/json HTML-escapes the
	// angle brackets, so the raw bytes read \u003credacted\u003e.
	var entry map[string]any
	if err := json.Unmarshal(b, &entry); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, b)
	}
	payload := entry["payload"].(map[string]any)
	data := payload["data"].(map[string]any)
	if data["value"] != "<redacted>" {
		t.Errorf("value = %v, want <redacted>", data["value"])
	}
	if strings.Contains(string(b), "clientSecret.secured\",\"value\":\"s3cr3t") {
		t.Error("a secret reached requests.log")
	}
}
