package migrate

import (
	"log/slog"

	sqapi "github.com/sonar-solutions/sq-api-go"
)

// httpDebugLogger returns a DebugLogFunc that emits one slog Debug entry per
// HTTP request/response pair. Authorization is already redacted by the SDK
// before this callback runs. Bodies are logged verbatim (assumed JSON or
// form-encoded), capped only by slog's own formatting.
func httpDebugLogger(logger *slog.Logger) sqapi.DebugLogFunc {
	return func(method, url string, headers map[string][]string, reqBody []byte, respStatus int, respBody []byte, err error) {
		args := []any{
			"method", method,
			"url", url,
			"headers", headers,
		}
		if len(reqBody) > 0 {
			args = append(args, "request_body", string(reqBody))
		}
		if err != nil {
			args = append(args, "err", err.Error())
			logger.Debug("http request failed", args...)
			return
		}
		args = append(args, "response_status", respStatus)
		if len(respBody) > 0 {
			args = append(args, "response_body", string(respBody))
		}
		logger.Debug("http request", args...)
	}
}
