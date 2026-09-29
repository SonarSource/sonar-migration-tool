// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"net/url"
)

func webhookTasks() []TaskDef {
	return []TaskDef{
		{Name: "getWebhooks", Editions: AllEditions,
			Run: func(ctx context.Context, e *Executor) error {
				return fetchAndWriteArray(ctx, e, "getWebhooks", "api/webhooks/list", "webhooks", nil, map[string]any{"serverUrl": e.ServerURL})
			}},
		{Name: "getServerWebhooks", Editions: AllEditions,
			Run: func(ctx context.Context, e *Executor) error {
				return fetchAndWriteArray(ctx, e, "getServerWebhooks", "api/webhooks/list", "webhooks", nil, map[string]any{"serverUrl": e.ServerURL})
			}},
		{Name: "getWebhookDeliveries", Editions: AllEditions, Dependencies: []string{"getWebhooks"},
			Run: webhookDeliveries("getWebhookDeliveries", "getWebhooks")},
		{Name: "getProjectWebhookDeliveries", Editions: AllEditions, Dependencies: []string{"getProjectWebhooks"},
			Run: webhookDeliveries("getProjectWebhookDeliveries", "getProjectWebhooks")},
	}
}

// webhookDeliveries fetches deliveries for each webhook from a dependency task.
func webhookDeliveries(taskName, depTask string) func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, taskName, depTask,
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				webhookKey := extractField(item, "key")
				items, err := e.Raw.GetPaginated(ctx, PaginatedOpts{
					Path: "api/webhooks/deliveries", ResultKey: "deliveries", MaxPageSize: 500, PageLimit: 10,
					Params: url.Values{"webhook": {webhookKey}},
					// The 10-page cap is a deliberate 5,000-row sample
					// of a delivery log that grows without bound, not a
					// workaround for the 10,000-result ceiling. Without
					// this flag every instance with a busy webhook would
					// get a data-loss bullet in its migration report for
					// deliveries nobody asked to migrate (#574).
					SamplingCap: true,
				})
				if err != nil {
					return err
				}
				return w.WriteChunk(enrichAll(items, map[string]any{"webhookKey": webhookKey, "serverUrl": e.ServerURL}))
			})
	}
}
