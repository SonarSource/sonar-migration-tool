// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"net/url"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// aiCodeFixV2Available marks the SQS release where
// api/v2/fix-suggestions/feature-enablements first answered GET
// requests. 2025.1 / 2025.2 return HTTP 405 for the GET, so the task
// is skipped entirely on those servers (issue #372).
var aiCodeFixV2Available = common.MustParseVersion("2025.3")

func systemTasks() []TaskDef {
	return []TaskDef{
		{
			Name:     "getServerInfo",
			Editions: AllEditions,
			Run: func(ctx context.Context, e *Executor) error {
				return fetchAndWriteSingle(ctx, e, "getServerInfo", "api/system/info", nil, "", map[string]any{"serverUrl": e.ServerURL})
			},
		},
		{
			Name:     "getServerSettings",
			Editions: AllEditions,
			Run: func(ctx context.Context, e *Executor) error {
				return fetchAndWriteArray(ctx, e, "getServerSettings", "api/settings/values", "settings", nil, map[string]any{"serverUrl": e.ServerURL})
			},
		},
		{
			// Companion to getServerSettings: extracts the SQS-side
			// setting definitions (key, type, multiValues, defaultValue)
			// so the migrate phase can decide which global settings have
			// been customized (value != defaultValue — issue #186).
			Name:     "getServerSettingsDefinitions",
			Editions: AllEditions,
			Run: func(ctx context.Context, e *Executor) error {
				return fetchAndWriteArray(ctx, e, "getServerSettingsDefinitions", "api/settings/list_definitions", "definitions", nil, map[string]any{"serverUrl": e.ServerURL})
			},
		},
		{
			Name:     "getPlugins",
			Editions: AllEditions,
			Run: func(ctx context.Context, e *Executor) error {
				return fetchAndWriteArray(ctx, e, "getPlugins", "api/plugins/installed", "plugins",
					url.Values{"type": {"EXTERNAL"}}, map[string]any{"serverUrl": e.ServerURL})
			},
		},
		{
			Name:     "getUsage",
			Editions: []Edition{EditionDeveloper, EditionEnterprise, EditionDatacenter},
			Run: func(ctx context.Context, e *Executor) error {
				return fetchAndWriteArray(ctx, e, "getUsage", "api/projects/license_usage", "projects", nil, nil)
			},
		},
		{
			Name:     "getBindings",
			Editions: AllEditions,
			Run: func(ctx context.Context, e *Executor) error {
				return fetchAndWriteArray(ctx, e, "getBindings", "api/alm_settings/list", "almSettings", nil, map[string]any{"serverUrl": e.ServerURL})
			},
		},
		{
			// AI Code Fix configuration (issue #251). Single JSON
			// object exposing the global enablement state, the list
			// of enabled project keys, and the configured providers
			// (each carrying type, selected/selfHosted flags, and the
			// chosen model). The migrate + predict pipelines combine
			// this with sonar.ai.codefix.hidden to drive the per-key
			// migration strategy. The endpoint was added in SQS 2025.3;
			// 2025.1 / 2025.2 return 405 for the GET and pre-2025
			// servers return 404 — both are skipped (issue #372).
			Name:     "getAiCodeFixConfig",
			Editions: AllEditions,
			Run: func(ctx context.Context, e *Executor) error {
				if e.Version.Less(aiCodeFixV2Available) {
					e.Logger.Debug("getAiCodeFixConfig skipped: SQS version predates api/v2/fix-suggestions/feature-enablements",
						"version", e.Version.String())
					return nil
				}
				err := fetchAndWriteSingle(ctx, e, "getAiCodeFixConfig",
					"api/v2/fix-suggestions/feature-enablements", nil, "",
					map[string]any{"serverUrl": e.ServerURL})
				if err != nil && isNonFatalHTTPErr(err) {
					return nil
				}
				return err
			},
		},
	}
}
