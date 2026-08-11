# sq-api-go

Typed Go client library for the SonarQube Server and SonarQube Cloud APIs. Scoped to the endpoints needed by the [sonar-migration-tool](https://github.com/sonar-solutions/sonar-migration-tool).

## Installation

```bash
go get github.com/sonar-solutions/sq-api-go
```

## Quick Start

### SonarQube Server

```go
import (
    sqapi "github.com/sonar-solutions/sq-api-go"
    "github.com/sonar-solutions/sq-api-go/server"
)

// Create a base client (version determines auth strategy)
base := sqapi.NewServerClient("https://sonar.example.com", "squ_mytoken", 10.7)
sc := server.New(base)

// Fetch all projects (paginated automatically)
projects, err := sc.Projects.Search(ctx).All(ctx)

// Get system info
info, err := sc.System.Info(ctx)
version, err := sc.System.Version(ctx)
```

### SonarQube Cloud

```go
import (
    sqapi "github.com/sonar-solutions/sq-api-go"
    "github.com/sonar-solutions/sq-api-go/cloud"
)

base := sqapi.NewCloudClient("https://sonarcloud.io", "squ_mytoken")
cc := cloud.New(base)

// Create a project
proj, err := cc.Projects.Create(ctx, cloud.CreateProjectParams{
    ProjectKey:   "org_my-project",
    Name:         "My Project",
    Organization: "my-org",
    Visibility:   "private",
})

// Create a quality gate with conditions
gate, err := cc.QualityGates.Create(ctx, "My Gate", "my-org")
_, err = cc.QualityGates.CreateCondition(ctx, cloud.CreateConditionParams{
    GateID:       gate.ID,
    Organization: "my-org",
    Metric:       "new_coverage",
    Op:           "LT",
    Error:        "80",
})
```

### Mutual TLS (mTLS)

```go
base := sqapi.NewServerClient(
    "https://sonar.example.com", "squ_mytoken", 9.9,
    sqapi.WithClientCert("/path/to/cert.pem", "/path/to/key.pem", ""),
)
// Check for certificate loading errors
if err := base.CertErr(); err != nil {
    log.Fatal(err)
}
```

## Authentication

Auth strategy is selected by server version:

| Version | Auth Method |
|---------|------------|
| Server < 10.0 | HTTP Basic (`base64(token:)`) |
| Server >= 10.0 | Bearer token |
| Cloud | Bearer token |

The version is passed when constructing the client. Use `sqapi.ParseServerVersion()` to parse the response from `/api/server/version`:

```go
version, err := sqapi.ParseServerVersion("10.7.0.123") // returns 10.7
```

## Pagination

Paginated endpoints return a `*sqapi.Paginator[T]`. Fetch all results at once, or iterate page by page:

```go
// All at once
projects, err := sc.Projects.Search(ctx).All(ctx)

// Page by page (range-over-function, Go 1.23+)
for items, err := range sc.Projects.Search(ctx).Pages(ctx) {
    if err != nil {
        return err
    }
    for _, project := range items {
        fmt.Println(project.Key)
    }
}
```

Default page size is 500 (SonarQube maximum).

## Error Handling

Non-2xx responses return `*sqapi.APIError` with status code, method, URL, and response body:

```go
projects, err := sc.Projects.Search(ctx).All(ctx)
if err != nil {
    if sqapi.IsNotFound(err) {
        // 404
    } else if sqapi.IsUnauthorized(err) {
        // 401 - bad token
    } else if sqapi.IsForbidden(err) {
        // 403 - insufficient permissions
    }
}
```

## Available Endpoints

### Server Client (`server.New(base)`)

| Sub-client | Methods |
|-----------|---------|
| Projects | Search, GetDetails, LicenseUsage, Tags |
| Users | Search |
| Groups | Search, Users |
| Rules | Search, Show, Repositories |
| System | Info, Version |
| QualityGates | List, Show, SearchGroups, SearchUsers |
| QualityProfiles | Search, Backup, SearchGroups, SearchUsers |
| Permissions | SearchTemplates, TemplateGroups, TemplateUsers |
| Branches | List |
| PullRequests | List |
| Analyses | Search |
| Issues | Search |
| Hotspots | Search |
| Measures | Search |
| Settings | Values |
| Plugins | Installed |
| Views | Search, Show |
| Webhooks | List |
| Tokens | Search |
| NewCode | List |
| ALM | ListSettings, GetBinding |

### Cloud Client (`cloud.New(base)`)

| Sub-client | Methods |
|-----------|---------|
| Projects | Create, Delete, SetTags |
| Groups | Create, Delete, AddUser, Search |
| QualityProfiles | Create, Restore, Delete, SetDefault, ChangeParent, AddProject, AddGroup |
| QualityGates | Create, CreateCondition, Destroy, Select, SetDefault |
| Permissions | CreateTemplate, DeleteTemplate, SetDefaultTemplate, AddGroup, AddGroupToTemplate |
| Branches | Rename |
| Rules | Update |
| Settings | Set, SetValues |
| Enterprises | List, CreatePortfolio, UpdatePortfolio, DeletePortfolio |
| DOP | CreateProjectBinding |

## Configuration Options

```go
// Custom connection pool size (default: 50)
sqapi.WithMaxConnections(100)

// Custom request timeout in seconds (default: 60)
sqapi.WithTimeout(120)

// Mutual TLS client certificate
sqapi.WithClientCert("cert.pem", "key.pem", "")
```

## Architecture

The library uses a layered transport stack:

```
authTransport  → injects Authorization header
retryTransport → 3-attempt exponential backoff (retries 429/5xx)
http.Transport → base TCP/TLS transport
```

### Retry contract
<!-- updated: 2026-08-11_11:35:00 -->

`retryTransport` guarantees the following to its callers:

- **Giving up preserves the response.** When a retryable status (429/500/502/503/504) survives the whole schedule, the transport returns that response with a **readable body** and a nil error, so the caller can build a meaningful error from the real status and payload. Any bytes the 429 classifier peeked at are spliced back in front of the remainder, so the caller reads the payload byte-for-byte. (Closing the body here was the cause of issue #505: `http: read on closed response body`.)
- **Retrying releases the connection.** On the retry path only, the body is drained and closed so the connection returns to the pool.
- **Retried requests re-send their body.** `req.Body` is rewound via `http.Request.GetBody` before each re-attempt. A request that carries a body but has no `GetBody` is never retried — re-issuing it would transmit an empty payload. Use `strings.Reader`, `bytes.Reader` or `bytes.Buffer` as the body (as every write helper in `cloud/` does) so `http.NewRequest` populates `GetBody` automatically.

Response types are defined in the `types` sub-package. The `server` and `cloud` sub-packages provide typed endpoint methods that return these types.

## License

Internal use only. See repository root for license information.
