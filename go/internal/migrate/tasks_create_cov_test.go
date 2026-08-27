// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"testing"
)

// --- extractAnyStr (was 84.6%) ---

func TestExtractAnyStrEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		key  string
		want string
	}{
		{name: "string value", raw: `{"v":"hello"}`, key: "v", want: "hello"},
		{name: "integer value", raw: `{"v":30}`, key: "v", want: "30"},
		{name: "float value", raw: `{"v":1.5}`, key: "v", want: "1.5"},
		{name: "missing key", raw: `{"v":"x"}`, key: "other", want: ""},
		{name: "bad json", raw: `not json`, key: "v", want: ""},
		{name: "bool value not coercible", raw: `{"v":true}`, key: "v", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractAnyStr(json.RawMessage(tc.raw), tc.key); got != tc.want {
				t.Errorf("extractAnyStr(%s, %q) = %q, want %q", tc.raw, tc.key, got, tc.want)
			}
		})
	}
}

// --- resolveEnterpriseID (was 83.3%) ---

func TestResolveEnterpriseID(t *testing.T) {
	t.Run("array shape match", func(t *testing.T) {
		e := newCreateExecutorOnly(t)
		writeRawTask(t, e, "getEnterprises",
			`[{"id":"ent-uuid","key":"test-enterprise"},{"id":"other","key":"nope"}]`)
		id, err := resolveEnterpriseID(e)
		if err != nil || id != "ent-uuid" {
			t.Fatalf("got (%q, %v), want (ent-uuid, nil)", id, err)
		}
	})

	t.Run("flat object shape match", func(t *testing.T) {
		e := newCreateExecutorOnly(t)
		writeRawTask(t, e, "getEnterprises", `{"id":"flat-uuid","key":"test-enterprise"}`)
		id, err := resolveEnterpriseID(e)
		if err != nil || id != "flat-uuid" {
			t.Fatalf("got (%q, %v), want (flat-uuid, nil)", id, err)
		}
	})

	t.Run("no match returns error", func(t *testing.T) {
		e := newCreateExecutorOnly(t)
		writeRawTask(t, e, "getEnterprises", `[{"id":"x","key":"different"}]`)
		if _, err := resolveEnterpriseID(e); err == nil {
			t.Fatal("expected error when no enterprise matches EntKey")
		}
	})
}

// --- create-task hard-failure branches (non-already-exists → counter.Fail) ---

// A 403 (not "already exists") from every create endpoint must be swallowed
// per-item; the tasks return nil and simply produce no output rows.
func TestCreateTasksHardFailureProducesNoOutput(t *testing.T) {
	cases := []struct{ mapping, create string }{
		{"generateProjectMappings", "createProjects"},
		{"generateProfileMappings", "createProfiles"},
		{"generateGateMappings", "createGates"},
		{"generateGroupMappings", "createGroups"},
		{"generateTemplateMappings", "createPermissionTemplates"},
	}
	for _, tc := range cases {
		t.Run(tc.create, func(t *testing.T) {
			cloudSrv := newFailingCloudServer() // 403 on every POST
			t.Cleanup(cloudSrv.Close)
			apiSrv := newMockAPIServer()
			t.Cleanup(apiSrv.Close)
			dir := t.TempDir()
			setupExtractData(dir)
			e := newTestExecutor(cloudSrv, apiSrv, dir)
			setupCSVs(t, dir)
			runTask(t, e, tc.mapping)

			reg := BuildMigrateRegistry(RegisterAll())
			if err := reg[tc.create].Run(context.Background(), e); err != nil {
				t.Fatalf("%s: a per-item 403 must not fail the task, got %v", tc.create, err)
			}
			items, _ := e.Store.ReadAll(tc.create)
			if len(items) != 0 {
				t.Errorf("%s: expected no output on hard failure, got %d", tc.create, len(items))
			}
		})
	}
}

// --- already-exists BUT lookup also fails (counter.Fail + no output) ---

// When create returns "already exists" and the follow-up lookup also errors,
// the profile/gate/group/template tasks record no output row.
func TestCreateTasksAlreadyExistsLookupFails(t *testing.T) {
	cases := []struct{ mapping, create string }{
		{"generateProfileMappings", "createProfiles"},
		{"generateGateMappings", "createGates"},
		{"generateGroupMappings", "createGroups"},
		{"generateTemplateMappings", "createPermissionTemplates"},
	}
	for _, tc := range cases {
		t.Run(tc.create, func(t *testing.T) {
			cloudSrv := newAlreadyExistsButLookupFailsServer()
			t.Cleanup(cloudSrv.Close)
			apiSrv := newMockAPIServer()
			t.Cleanup(apiSrv.Close)
			dir := t.TempDir()
			setupExtractData(dir)
			e := newTestExecutor(cloudSrv, apiSrv, dir)
			setupCSVs(t, dir)
			runTask(t, e, tc.mapping)

			reg := BuildMigrateRegistry(RegisterAll())
			if err := reg[tc.create].Run(context.Background(), e); err != nil {
				t.Fatalf("%s: lookup failure must not fail the task, got %v", tc.create, err)
			}
			items, _ := e.Store.ReadAll(tc.create)
			if len(items) != 0 {
				t.Errorf("%s: expected no output when lookup fails, got %d: %s",
					tc.create, len(items), items)
			}
		})
	}
}

// --- runCreatePortfolios: enterprise not resolvable → task returns error ---

func TestRunCreatePortfoliosNoEnterprise(t *testing.T) {
	e, dir := newCreateTest(t)
	setupCSVs(t, dir)
	runTask(t, e, "generatePortfolioMappings")
	// getEnterprises output has a non-matching key, so resolveEnterpriseID fails.
	writeRawTask(t, e, "getEnterprises", `[{"id":"x","key":"different-ent"}]`)

	reg := BuildMigrateRegistry(RegisterAll())
	if err := reg["createPortfolios"].Run(context.Background(), e); err == nil {
		t.Fatal("expected createPortfolios to error when the enterprise can't be resolved")
	}
}

// --- test-only helpers ---

// newCreateExecutorOnly returns a minimal Executor with a run store and the
// EntKey used by resolveEnterpriseID, without any mock HTTP servers.
func newCreateExecutorOnly(t *testing.T) *Executor {
	t.Helper()
	cloudSrv := newMockCloudServer()
	t.Cleanup(cloudSrv.Close)
	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)
	return newTestExecutor(cloudSrv, apiSrv, t.TempDir())
}

// writeRawTask writes a single raw JSON blob as one JSONL record for a task.
func writeRawTask(t *testing.T, e *Executor, task, raw string) {
	t.Helper()
	w, err := e.Store.Writer(task)
	if err != nil {
		t.Fatalf("writer(%s): %v", task, err)
	}
	if err := w.WriteOne(json.RawMessage(raw)); err != nil {
		t.Fatalf("write(%s): %v", task, err)
	}
}
