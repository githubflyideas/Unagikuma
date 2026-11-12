package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// failure is a delivery that did not work, with what the service said
// about trying again.
type failure struct {
	msg string
	// permanent: the same request fails the same way every time (a wrong
	// token, a message the service refuses), so retrying is pointless.
	permanent bool
	// retryAfter: the service asked to wait this long (rate limit).
	retryAfter time.Duration
}

func (f *failure) Error() string { return f.msg }

// classify fills the attempt from a delivery error.
func classify(a *Attempt, err error) {
	a.Error = err.Error()
	var f *failure
	if errors.As(err, &f) {
		a.Permanent, a.RetryAfter = f.permanent, f.retryAfter
	}
}

// codeRule says what one of a service's own error codes means.
type codeRule struct {
	permanent bool
	wait      time.Duration
}

var (
	permanentCode = codeRule{permanent: true}
	// The chat robots of DingTalk and WeCom take 20 messages a minute; a
	// robot that goes on sending is blocked for longer.
	minuteLimit = codeRule{wait: time.Minute}
)

// Error codes the services answer with (often under HTTP 200). Codes not
// listed are taken as temporary.
var serviceCodes = map[string]map[string]codeRule{
	"feishu": {
		"19001": permanentCode, // webhook token invalid
		"19002": permanentCode, // message format
		"19021": permanentCode, // signature or timestamp
		"19022": permanentCode, // caller's IP not allowed
		"19024": permanentCode, // keyword not in the message
		"11232": {wait: 10 * time.Second},
	},
	"dingtalk": {
		"300001": permanentCode, "300005": permanentCode, // token
		"310000": permanentCode,                         // keyword, signature or IP check
		"400013": permanentCode, "40035": permanentCode, // message format
		"460101": permanentCode, // message too long
		"130101": minuteLimit,
	},
	"wecom": {
		"93000": permanentCode,                                                 // webhook key invalid
		"40008": permanentCode, "44004": permanentCode, "40058": permanentCode, // message format or length
		"45009": minuteLimit,
	},
}

// statusFailure classifies an HTTP status that is not a success.
func statusFailure(status int, h http.Header, body []byte) *failure {
	f := &failure{msg: fmt.Sprintf("HTTP %d", status)}
	if why := replyReason(body); why != "" {
		f.msg += ": " + why
	}
	switch status {
	case 400, 401, 403, 404, 405, 410, 413, 422:
		f.permanent = true
	case 429:
		f.retryAfter = retryAfter(h, body)
	}
	return f
}

// retryAfter reads how long a rate-limited service asks to wait: the
// Retry-After header (seconds or a date), or the field Telegram and
// Discord put in the body.
func retryAfter(h http.Header, body []byte) time.Duration {
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			return time.Duration(n * float64(time.Second))
		}
		if t, err := http.ParseTime(v); err == nil {
			if d := time.Until(t); d > 0 {
				return d
			}
		}
	}
	var j struct {
		RetryAfter float64 `json:"retry_after"`
		Parameters struct {
			RetryAfter float64 `json:"retry_after"`
		} `json:"parameters"`
	}
	if json.Unmarshal(body, &j) == nil {
		if n := max(j.RetryAfter, j.Parameters.RetryAfter); n > 0 {
			return time.Duration(n * float64(time.Second))
		}
	}
	return 0
}

// replyReason is the service's own words for a refusal, from the usual
// fields of a JSON reply or a short plain-text one.
func replyReason(body []byte) string {
	var m map[string]any
	if json.Unmarshal(body, &m) == nil {
		for _, k := range []string{"description", "message", "errmsg", "msg", "error", "errors", "Message"} {
			switch v := m[k].(type) {
			case string:
				if v != "" {
					return sanitize(strings.Join(strings.Fields(v), " "), 160)
				}
			case []any:
				if len(v) > 0 {
					return sanitize(strings.Join(strings.Fields(fmt.Sprint(v[0])), " "), 160)
				}
			case map[string]any:
				if s, ok := v["message"].(string); ok && s != "" {
					return sanitize(strings.Join(strings.Fields(s), " "), 160)
				}
			}
		}
		return ""
	}
	s := strings.Join(strings.Fields(string(body)), " ")
	if s == "" || strings.HasPrefix(s, "<") {
		return "" // an HTML error page says nothing useful
	}
	return sanitize(s, 160)
}
