// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import "testing"

func TestIssueSourceLinkURL(t *testing.T) {
	cases := []struct {
		name       string
		serverURL  string
		projectKey string
		issueKey   string
		branch     string
		want       string
	}{
		{
			name: "basic (no branch)", serverURL: "https://sq.example.com", projectKey: "my-proj", issueKey: "AYx1",
			want: "https://sq.example.com/project/issues?id=my-proj&issues=AYx1&open=AYx1",
		},
		{
			name: "with branch", serverURL: "https://sq.example.com", projectKey: "p", issueKey: "k", branch: "feature/x",
			want: "https://sq.example.com/project/issues?id=p&issues=k&open=k&branch=feature%2Fx",
		},
		{
			name: "trailing slash trimmed", serverURL: "https://sq.example.com/", projectKey: "p", issueKey: "k",
			want: "https://sq.example.com/project/issues?id=p&issues=k&open=k",
		},
		{
			name: "special chars escaped", serverURL: "https://sq.example.com", projectKey: "org:proj", issueKey: "a b",
			want: "https://sq.example.com/project/issues?id=org%3Aproj&issues=a+b&open=a+b",
		},
		{name: "missing serverURL", serverURL: "", projectKey: "p", issueKey: "k", want: ""},
		{name: "missing projectKey", serverURL: "https://sq", projectKey: "", issueKey: "k", want: ""},
		{name: "missing issueKey", serverURL: "https://sq", projectKey: "p", issueKey: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := issueSourceLinkURL(tc.serverURL, tc.projectKey, tc.issueKey, tc.branch); got != tc.want {
				t.Fatalf("issueSourceLinkURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHotspotSourceLinkURL(t *testing.T) {
	cases := []struct {
		name       string
		serverURL  string
		projectKey string
		hotspotKey string
		branch     string
		want       string
	}{
		{
			name: "basic (no branch)", serverURL: "https://sq.example.com", projectKey: "my-proj", hotspotKey: "HS-9",
			want: "https://sq.example.com/security_hotspots?id=my-proj&hotspots=HS-9",
		},
		{
			name: "with branch", serverURL: "https://sq.example.com", projectKey: "p", hotspotKey: "k", branch: "release/2.0",
			want: "https://sq.example.com/security_hotspots?id=p&hotspots=k&branch=release%2F2.0",
		},
		{
			name: "trailing slash trimmed", serverURL: "https://sq.example.com/", projectKey: "p", hotspotKey: "k",
			want: "https://sq.example.com/security_hotspots?id=p&hotspots=k",
		},
		{name: "missing serverURL", serverURL: "", projectKey: "p", hotspotKey: "k", want: ""},
		{name: "missing projectKey", serverURL: "https://sq", projectKey: "", hotspotKey: "k", want: ""},
		{name: "missing hotspotKey", serverURL: "https://sq", projectKey: "p", hotspotKey: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hotspotSourceLinkURL(tc.serverURL, tc.projectKey, tc.hotspotKey, tc.branch); got != tc.want {
				t.Fatalf("hotspotSourceLinkURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProjectSourceLinkURL(t *testing.T) {
	cases := []struct {
		name       string
		serverURL  string
		projectKey string
		want       string
	}{
		{
			name: "basic", serverURL: "https://sq.example.com", projectKey: "my-proj",
			want: "https://sq.example.com/dashboard?id=my-proj",
		},
		{
			name: "trailing slash trimmed", serverURL: "https://sq.example.com/", projectKey: "p",
			want: "https://sq.example.com/dashboard?id=p",
		},
		{
			name: "special chars escaped", serverURL: "https://sq.example.com", projectKey: "org:proj",
			want: "https://sq.example.com/dashboard?id=org%3Aproj",
		},
		{name: "missing serverURL", serverURL: "", projectKey: "p", want: ""},
		{name: "missing projectKey", serverURL: "https://sq", projectKey: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectSourceLinkURL(tc.serverURL, tc.projectKey); got != tc.want {
				t.Fatalf("projectSourceLinkURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSourceLinkIdempotencyHelpers(t *testing.T) {
	link := issueSourceLinkURL("https://sq.example.com", "p", "k", "")

	t.Run("issue: marker detected even when URL is HTML-escaped", func(t *testing.T) {
		// Simulates the cloud API returning the comment with the URL's "&"
		// escaped — the marker still matches, preventing a duplicate link.
		cloud := []issueComment{
			{Markdown: "[Migrated from alice] some text"},
			{HTMLText: issueSourceLinkMarker + "(https://sq.example.com/project/issues?id=p&amp;issues=k&amp;open=k)"},
		}
		if !issueCommentsContain(cloud, issueSourceLinkMarker) {
			t.Fatal("expected the existing source link to be detected via marker")
		}
	})
	t.Run("issue: absent when no link", func(t *testing.T) {
		cloud := []issueComment{{HTMLText: "just a migrated comment"}}
		if issueCommentsContain(cloud, issueSourceLinkMarker) {
			t.Fatal("did not expect a source link to be detected")
		}
	})
	_ = link

	t.Run("hotspot: marker detected", func(t *testing.T) {
		cloud := []hotspotComment{{Markdown: hotspotSourceLinkMarker + "(https://sq.example.com/security_hotspots?id=p&hotspots=k)"}}
		if !hotspotCommentsContain(cloud, hotspotSourceLinkMarker) {
			t.Fatal("expected the existing hotspot source link to be detected via marker")
		}
	})
	t.Run("hotspot: absent when no link", func(t *testing.T) {
		cloud := []hotspotComment{{HTMLText: "unrelated"}}
		if hotspotCommentsContain(cloud, hotspotSourceLinkMarker) {
			t.Fatal("did not expect a hotspot source link to be detected")
		}
	})
}
