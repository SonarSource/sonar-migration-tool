// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package regtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// urlParams creates a url.Values from key-value pairs.
func urlParams(kvs ...string) url.Values {
	p := url.Values{}
	for i := 0; i+1 < len(kvs); i += 2 {
		p.Set(kvs[i], kvs[i+1])
	}
	return p
}

// queryTotal queries an API endpoint and extracts the total count from the response.
func queryTotal(ctx context.Context, raw *common.RawClient, path string, params url.Values) (int, error) {
	if params == nil {
		params = url.Values{}
	}
	params.Set("ps", "1")
	body, err := raw.Get(ctx, path, params)
	if err != nil {
		return 0, err
	}
	return common.ExtractTotal(body, "paging.total"), nil
}

// queryCount queries an API endpoint and counts elements in an array at the given key.
func queryCount(ctx context.Context, raw *common.RawClient, path string, params url.Values, arrayKey string) (int, error) {
	body, err := raw.Get(ctx, path, params)
	if err != nil {
		return 0, err
	}
	items, err := common.ExtractArray(body, arrayKey)
	if err != nil {
		return 0, err
	}
	return len(items), nil
}

// queryJSON queries an API endpoint and returns the raw JSON response.
func queryJSON(ctx context.Context, raw *common.RawClient, path string, params url.Values) (json.RawMessage, error) {
	return raw.Get(ctx, path, params)
}

// makeResult creates a single check result comparing SQS and SC integer counts.
func makeResult(category, name string, sqsCount, scCount int, tolerance string) CheckResult {
	return CheckResult{
		Category:  category,
		Name:      name,
		SQSValue:  strconv.Itoa(sqsCount),
		SCValue:   strconv.Itoa(scCount),
		Match:     sqsCount == scCount,
		Tolerance: tolerance,
	}
}

// makeResultStr creates a check result comparing string values.
func makeResultStr(category, name, sqsVal, scVal, tolerance string) CheckResult {
	return CheckResult{
		Category:  category,
		Name:      name,
		SQSValue:  sqsVal,
		SCValue:   scVal,
		Match:     sqsVal == scVal,
		Tolerance: tolerance,
	}
}

// makeError creates a check result representing an error.
func makeError(category, name string, err error) CheckResult {
	return CheckResult{
		Category: category,
		Name:     name,
		Error:    err.Error(),
	}
}

// makeSkipped creates a skipped check result.
func makeSkipped(category, name, reason string) CheckResult {
	return CheckResult{
		Category: category,
		Name:     name,
		Match:    true,
		Notes:    "SKIPPED",
		SQSValue: reason,
		SCValue:  "N/A",
	}
}

// unmarshalField extracts a field value from raw JSON.
func unmarshalField(raw json.RawMessage, key string) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	val, ok := obj[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(val, &s); err != nil {
		return string(val)
	}
	return s
}

// withNote adds a note to a check result.
func withNote(r CheckResult, note string) CheckResult {
	r.Notes = note
	return r
}

// withSqsAndSqcFeatureDivergence marks a mismatch as a known SQS_AND_SQC_FEATURE_DIVERGENCE
// (a permanent, by-design difference between SonarQube Server and SonarQube
// Cloud) rather than a migration defect — see
// CheckResult.SqsAndSqcFeatureDivergence. note must state plainly what SQS
// shows, what SQC shows, and the product reason they differ, so a reader
// never has to guess whether this is a bug.
func withSqsAndSqcFeatureDivergence(r CheckResult, note string) CheckResult {
	r.SqsAndSqcFeatureDivergence = true
	r.Notes = note
	return r
}

// countWithFilter queries an issues/hotspots endpoint with a filter parameter.
func countWithFilter(ctx context.Context, raw *common.RawClient, path string, baseParams url.Values, filterKey, filterValue string) (int, error) {
	params := common.CloneParams(baseParams)
	params.Set(filterKey, filterValue)
	return queryTotal(ctx, raw, path, params)
}

// extractStringArray extracts a string array from a JSON field.
func extractStringArray(raw json.RawMessage, key string) []string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	val, ok := obj[key]
	if !ok {
		return nil
	}
	var arr []string
	if err := json.Unmarshal(val, &arr); err != nil {
		return nil
	}
	return arr
}

// intToStr converts an int to string for display.
func intToStr(n int) string {
	return fmt.Sprintf("%d", n)
}
