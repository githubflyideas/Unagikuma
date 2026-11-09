// Package notify renders and delivers one message through one channel.
// The fallback chain, intents (crash safety), rate limiting and caps live in
// the engine; this package only knows how to talk to each destination and
// how to tell success from failure.
package notify

import (
	"encoding/json"
	"github.com/githubflyideas/unagikuma/internal/observation"
	"time"
)

// Message is one notification.
type Message struct {
	// Brand replaces the product name in rendered messages (white label).
	Brand            string                `json:"brand,omitempty"`
	GlobalIncidentID string                `json:"incident_global_id,omitempty"`
	Observations     *observation.Snapshot `json:"observations,omitempty"`
	Kind             string                `json:"kind"`     // PROBLEM REPEAT REOPEN RECOVERY DRILL SYSTEM DIGEST TEST
	Priority         string                `json:"priority"` // high, low
	Monitor          string                `json:"monitor"`
	Title            string                `json:"title"` // display name
	Type             string                `json:"type"`
	Target           string                `json:"target"`
	Depth            string                `json:"depth"`
	Status           string                `json:"status"`
	Reason           string                `json:"reason"`
	Detail           string                `json:"detail"`
	Quorum           string                `json:"quorum,omitempty"`
	Count            int                   `json:"count"`
	Max              int                   `json:"max"`
	IncidentID       int64                 `json:"incident_id"`
	Duration         time.Duration         `json:"duration"`
	DetailURL        string                `json:"detail_url,omitempty"`
	AckURL           string                `json:"ack_url,omitempty"`
	Tags             []string              `json:"tags,omitempty"`
	Time             time.Time             `json:"time"`
	Delayed          time.Duration         `json:"delayed,omitempty"` // >0 when delivered late
	DryRun           bool                  `json:"dry_run,omitempty"`
	Instance         string                `json:"instance"` // unagikuma instance name (multi-instance setups)
	// Site names the site (cluster node) that sent the message; empty on
	// a single machine. Sites never deduplicate each other's messages.
	Site       string `json:"site,omitempty"`
	DeliveryID string `json:"delivery_id"`    // idempotency key, also X-Unagikuma-Delivery-Id
	Lang       string `json:"lang"`           // zh or en
	Body       string `json:"body,omitempty"` // preformatted body for SYSTEM/DIGEST kinds
	// Class is what the failure is: business (the service answers wrongly),
	// network, uncertain (cannot be reached) or system.
	Class string `json:"class,omitempty"`
}

// Channel is a resolved channel definition (secrets already substituted).
type Channel struct {
	Name string
	Kind string // log webhook feishu dingtalk wecom slack discord telegram email
	Opts map[string]string
}

// Attempt is the outcome of delivering to one channel.
type Attempt struct {
	Channel  string        `json:"channel"`
	OK       bool          `json:"ok"`
	Error    string        `json:"error,omitempty"`
	Status   int           `json:"status,omitempty"`
	Latency  time.Duration `json:"-"`
	Response string        `json:"response,omitempty"` // redacted snippet
	// Bounces are recipients the provider said do not exist (mailbox
	// unknown, invalid number): the rest of the channel still got it.
	Bounces []Bounce `json:"bounces,omitempty"`
}

// Bounce is one recipient refused as nonexistent.
type Bounce struct {
	Address string `json:"address"` // e-mail address or phone number as configured
	Reason  string `json:"reason"`
}

// bounceError marks a failure that means the recipient does not exist.
type bounceError struct{ Bounce }

func (e *bounceError) Error() string { return e.Address + ": " + e.Reason }

type attemptJSON struct {
	Channel   string  `json:"channel"`
	OK        bool    `json:"ok"`
	Error     string  `json:"error,omitempty"`
	Status    int     `json:"status,omitempty"`
	LatencyMS float64 `json:"latency_ms"`
	Response  string  `json:"response,omitempty"`
	Latency   int64   `json:"latency,omitempty"` // nanoseconds, as stored before
}

// MarshalJSON writes the latency in milliseconds, like every API field.
func (a Attempt) MarshalJSON() ([]byte, error) {
	return json.Marshal(attemptJSON{Channel: a.Channel, OK: a.OK, Error: a.Error, Status: a.Status,
		LatencyMS: float64(a.Latency) / float64(time.Millisecond), Response: a.Response})
}

// UnmarshalJSON reads both forms, so attempts stored earlier keep their
// latency.
func (a *Attempt) UnmarshalJSON(b []byte) error {
	var j attemptJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	*a = Attempt{Channel: j.Channel, OK: j.OK, Error: j.Error, Status: j.Status, Response: j.Response,
		Latency: time.Duration(j.LatencyMS * float64(time.Millisecond))}
	if j.Latency != 0 {
		a.Latency = time.Duration(j.Latency)
	}
	return nil
}
