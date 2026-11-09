package engine

import (
	"fmt"
	"time"

	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
)

// Sink receives everything the engine wants persisted or broadcast. It is
// called from the engine loop only. SaveIncident and SaveIntent must be
// durable when they return: notification crash safety depends on it.
type Sink interface {
	// SaveState persists a monitor's state after it changed.
	SaveState(StateRecord)
	// DeleteState forgets a removed monitor.
	DeleteState(name string)
	// Verdict records one round (batched by the implementation).
	Verdict(VerdictRecord)
	// Evidence keeps the raw exchange of a round for the packet viewer.
	Evidence(name, node string, at time.Time, r probe.Result)
	// SaveIncident creates (ID == 0, the sink assigns it) or updates an
	// incident.
	SaveIncident(*Incident) error
	// SaveIntent creates or updates a notification intent.
	SaveIntent(*Intent) error
	// Delivery logs one finished delivery attempt chain.
	Delivery(DeliveryRecord)
	// Event records a typed event and broadcasts it.
	Event(Event)
	// Counter adds n to a named daily value counter.
	Counter(name string, n int)
}

// StateRecord is a monitor's persisted runtime state.
type StateRecord struct {
	Name       string
	State      MonState
	Status     string // display status
	Reason     string
	Detail     string
	LatencyMS  float64
	LastCheck  time.Time
	LastChange time.Time
	IncidentID int64
	DefHash    string
}

// VerdictRecord is one judged round.
type VerdictRecord struct {
	Monitor   string
	At        time.Time
	Verdict   VerdictKind
	Status    string // display status after the transition
	Hard      bool
	Reason    string
	LatencyMS float64
	FailNodes []string
	OKNodes   []string
	// Excluded marks rounds outside the uptime denominator (unknown,
	// pending, paused, local outage).
	Excluded bool
	// Interval is the monitor's check interval, used to tell a gap in the
	// history from the time between two rounds.
	Interval time.Duration
}

// Incident kinds.
const (
	IncidentNormal = "normal"
	IncidentLow    = "low"
	IncidentSystem = "system"
)

// System incident keys.
const (
	SystemLocalNetwork = "local_network"
	SystemOverload     = "overload"
	SystemAllFailed    = "all_failed"
	SystemStorage      = "storage" // results and events cannot be written
	SystemMaster       = "master"  // a replica cannot reach its master and checks on its own
)

// Incident is a problem with its notification state.
type Incident struct {
	SourceNode  string
	SourceEpoch string
	OriginalID  int64
	GlobalID    string
	ID          int64
	Monitor     string // "" for system incidents
	Kind        string // normal | low | system
	System      string // system incident key
	Policy      string
	Reason      string
	Detail      string
	Title       string
	// AckComment is the note given with the ACK.
	AckComment string
	// Summary is the body of a system incident's recovery notice.
	Summary string
	State   IncidentState
	// openClass is the reason class it opened with (Reason follows the
	// latest check); kept in memory only.
	openClass string
}

// Intent states.
const (
	IntentPending = "pending"
	IntentDone    = "done"
	IntentFailed  = "failed"
	IntentMerged  = "merged"
	IntentDropped = "dropped" // queued during a local outage, judged stale
)

// Intent is a crash-safe notification in flight.
type Intent struct {
	ID         string
	IncidentID int64
	Seq        int
	Kind       string // PROBLEM REPEAT REOPEN RECOVERY SYSTEM DIGEST DRILL TEST
	Policy     string
	Message    notify.Message
	Delivery   Delivery
	State      string
	CreatedAt  time.Time
	DueAt      time.Time
	UpdatedAt  time.Time
	// System marks messages about the monitoring system itself; they are
	// never merged into digests and never raise ALL_FAILED.
	System bool
	// ForceFail makes the first N channels fail without sending (chain drill).
	ForceFail int
	// Attempts collects the channel attempts for the notification log.
	Attempts []notify.Attempt
	// Chain replaces the policy's chain when set (escalations).
	Chain []string `json:"-"`
	// Covers: the incidents a digest stands for; its delivery counts as
	// telling each of them.
	Covers []int64 `json:"-"`
	// Evidence behind the alert's classification, for the ledger.
	Evidence map[string]any `json:"-"`
}

// DeliveryRecord is one row of the notification log.
type DeliveryRecord struct {
	IntentID   string
	IncidentID int64
	Kind       string
	Seq        int
	At         time.Time
	Attempts   []notify.Attempt
	OK         bool
	Fallback   bool
	Drill      bool
	Delayed    time.Duration
}

// Event types.
const (
	EventState    = "STATE"
	EventPartial  = "PARTIAL"
	EventIncident = "INCIDENT"
	EventNotify   = "NOTIFY"
	EventAck      = "ACK"
	EventSilence  = "SILENCE"
	EventConfig   = "CONFIG"
	EventSystem   = "SYSTEM"
	EventDrill    = "DRILL"
)

// Event is a typed, user-visible event.
type Event struct {
	At       time.Time
	Type     string
	Severity string // info | warning | critical
	Monitor  string
	Message  string
	Data     map[string]any
}

// Value counter names for the "blocked for you this week" card.
const (
	CounterSoftFiltered  = "soft_filtered"
	CounterOutageMerged  = "outage_merged"
	CounterOutageSaved   = "outage_saved"
	CounterCapSaved      = "cap_saved"
	CounterQuorumBlocked = "quorum_blocked"
)

func IncidentKey(node, epoch string, id int64) string {
	if node == "" || epoch == "" || id <= 0 {
		return ""
	}
	return fmt.Sprintf("%s:%s:%d", node, epoch, id)
}
