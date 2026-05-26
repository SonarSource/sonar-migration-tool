package cloud

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/sonar-solutions/sq-api-go/types"
)

// IssuesClient wraps the /api/issues/* endpoints.
type IssuesClient struct{ baseClient }

// SearchAll paginates /api/issues/search and returns all matching issues.
// Caller must set componentKeys, organization, and any status filters in params.
func (c *IssuesClient) SearchAll(ctx context.Context, params url.Values) ([]types.Issue, error) {
	if params.Get("ps") == "" {
		params.Set("ps", "500")
	}
	var all []types.Issue
	for page := 1; ; page++ {
		params.Set("p", fmt.Sprintf("%d", page))
		var resp types.IssuesSearchResponse
		if err := c.getJSON(ctx, "api/issues/search?"+params.Encode(), &resp); err != nil {
			return all, err
		}
		all = append(all, resp.Issues...)
		if len(all) >= resp.Paging.Total || len(resp.Issues) == 0 {
			break
		}
	}
	return all, nil
}

// DoTransition transitions an issue to a new status.
func (c *IssuesClient) DoTransition(ctx context.Context, issueKey, transition string) error {
	form := url.Values{}
	form.Set("issue", issueKey)
	form.Set("transition", transition)
	return c.postForm(ctx, "api/issues/do_transition", form, nil)
}

// AddComment adds a comment to an issue.
func (c *IssuesClient) AddComment(ctx context.Context, issueKey, text string) error {
	form := url.Values{}
	form.Set("issue", issueKey)
	form.Set("text", text)
	return c.postForm(ctx, "api/issues/add_comment", form, nil)
}

// SetTags sets the tags on an issue (replaces existing tags).
func (c *IssuesClient) SetTags(ctx context.Context, issueKey string, tags []string) error {
	form := url.Values{}
	form.Set("issue", issueKey)
	form.Set("tags", strings.Join(tags, ","))
	return c.postForm(ctx, "api/issues/set_tags", form, nil)
}
