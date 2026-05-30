package cloud

import (
	"context"
	"net/url"
)

// WebhooksClient wraps the SonarQube Cloud /api/webhooks/* endpoints.
type WebhooksClient struct{ baseClient }

// Webhook is a single webhook record returned by /api/webhooks/list.
type Webhook struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// ListWebhooksParams scopes a /api/webhooks/list call. Project is
// optional — omit it for org-scoped webhooks.
type ListWebhooksParams struct {
	Organization string
	Project      string
}

// listWebhooksResponse is the wire shape returned by /api/webhooks/list.
type listWebhooksResponse struct {
	Webhooks []Webhook `json:"webhooks"`
}

// List returns the webhooks visible at the given (organization,
// project) scope. The result is the raw list with no SQC-side
// filtering — the caller decides how to match.
func (c *WebhooksClient) List(ctx context.Context, params ListWebhooksParams) ([]Webhook, error) {
	q := url.Values{}
	q.Set("organization", params.Organization)
	if params.Project != "" {
		q.Set("project", params.Project)
	}
	var resp listWebhooksResponse
	if err := c.getJSON(ctx, "api/webhooks/list?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	return resp.Webhooks, nil
}

// CreateWebhookParams carries the per-webhook payload for
// /api/webhooks/create. Project + Organization are both required to
// register a project-scoped webhook on SonarQube Cloud; Secret is
// optional and only sent when non-empty.
type CreateWebhookParams struct {
	Organization string
	Project      string
	Name         string
	URL          string
	Secret       string
}

// Create registers a webhook on a SonarQube Cloud project.
func (c *WebhooksClient) Create(ctx context.Context, params CreateWebhookParams) error {
	form := url.Values{}
	form.Set("organization", params.Organization)
	form.Set("project", params.Project)
	form.Set("name", params.Name)
	form.Set("url", params.URL)
	if params.Secret != "" {
		form.Set("secret", params.Secret)
	}
	return c.postForm(ctx, "api/webhooks/create", form, nil)
}
