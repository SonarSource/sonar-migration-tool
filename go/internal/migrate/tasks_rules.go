// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	"github.com/sonar-solutions/sq-api-go/cloud"
)

// ruleTasks returns tasks for updating rules in Cloud.
func ruleTasks() []TaskDef {
	return []TaskDef{
		{
			Name:         "updateRuleTags",
			Dependencies: []string{"generateOrganizationMappings"},
			Run:          runUpdateRuleTags,
		},
		{
			Name:         "updateRuleDescriptions",
			Dependencies: []string{"generateOrganizationMappings"},
			Run:          runUpdateRuleDescriptions,
		},
	}
}

func runUpdateRuleTags(ctx context.Context, e *Executor) error {
	orgKeys := buildServerOrgLookup(e)

	counter := TaskCounterFromContext(ctx)
	err := forEachExtractItem(ctx, e, "updateRuleTags", "getRuleDetails",
		func(ctx context.Context, item structure.ExtractItem, w *common.ChunkWriter) error {
			ruleKey := extractField(item.Data, "key")
			tags := extractStringArray(item.Data, "tags")
			if ruleKey == "" || len(tags) == 0 {
				return nil
			}

			orgKey := orgKeys[item.ServerURL]
			_, err := e.Cloud.Rules.Update(ctx, cloud.UpdateRuleParams{
				Key:          ruleKey,
				Organization: orgKey,
				Tags:         strings.Join(tags, ","),
			})
			if err != nil {
				failAPI(counter, e.Logger, "updateRuleTags failed", err, "rule", ruleKey)
				return nil
			}
			counter.Success()
			_ = w.WriteOne(item.Data)
			return nil
		})
	return err
}

func runUpdateRuleDescriptions(ctx context.Context, e *Executor) error {
	orgKeys := buildServerOrgLookup(e)

	counter := TaskCounterFromContext(ctx)
	err := forEachExtractItem(ctx, e, "updateRuleDescriptions", "getRuleDetails",
		func(ctx context.Context, item structure.ExtractItem, w *common.ChunkWriter) error {
			ruleKey := extractField(item.Data, "key")
			note := extractField(item.Data, "mdNote")
			if ruleKey == "" || note == "" {
				return nil
			}

			orgKey := orgKeys[item.ServerURL]
			_, err := e.Cloud.Rules.Update(ctx, cloud.UpdateRuleParams{
				Key:          ruleKey,
				Organization: orgKey,
				MarkdownNote: note,
			})
			if err != nil {
				failAPI(counter, e.Logger, "updateRuleDescriptions failed", err, "rule", ruleKey)
				return nil
			}
			counter.Success()
			_ = w.WriteOne(item.Data)
			return nil
		})
	return err
}
