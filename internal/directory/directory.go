// Package directory checks the recipients of e-mail, SMS and voice
// channels against the contact directory: only active people in it are
// sent to. An address of someone who left, is on leave or was
// transferred, one that keeps bouncing, and one the directory does not
// know are left out and flagged, so mail and texts never reach former
// staff or a stray address.
package directory

import (
	"sort"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/store"
)

// Issue codes, in the order of how much they matter.
const (
	IssueUnknownHandle = "unknown_handle"  // @x is not in the directory: nothing sent for it
	IssueNoRecipients  = "no_recipients"   // nobody is left to send to
	IssueLeft          = "left"            // the address's owner left: skipped
	IssueInactive      = "inactive"        // the owner is on leave or transferred: skipped
	IssueBouncing      = "bouncing"        // refused as nonexistent too often: skipped
	IssueNoAddress     = "no_address"      // @x has no address of this kind
	IssueUnknown       = "unknown_address" // not in the directory: skipped
)

// Issue is one problem with one recipient of a channel.
type Issue struct {
	Code    string `json:"code"`
	Address string `json:"address"`          // as written in the channel
	Person  string `json:"person,omitempty"` // handle, when known
	Count   int    `json:"count,omitempty"`  // bounces
}

// Skips reports whether the recipient is left out of deliveries: every
// issue does.
func (i Issue) Skips() bool { return true }

// Directory is a snapshot of the directory and the bounce counts.
type Directory struct {
	people    []store.Person
	byHandle  map[string]*store.Person
	email     map[string]*store.Person
	bounces   map[string]store.BounceInfo // kind + "|" + normalized address
	threshold int                         // bounces before an address is skipped (0 = never)
}

// New builds a snapshot.
func New(people []store.Person, bounces []store.BounceInfo, threshold int) *Directory {
	d := &Directory{people: people, byHandle: map[string]*store.Person{}, email: map[string]*store.Person{},
		bounces: map[string]store.BounceInfo{}, threshold: threshold}
	for i := range d.people {
		p := &d.people[i]
		d.byHandle[p.Handle] = p
		for _, e := range p.Emails {
			d.email[store.NormalizeEmail(e)] = p
		}
	}
	for _, b := range bounces {
		d.bounces[b.Kind+"|"+b.Address] = b
	}
	return d
}

// People returns the directory entries.
func (d *Directory) People() []store.Person { return d.people }

// AddressKey is the channel option that holds recipients, and their kind,
// for channel kinds that send to people.
func AddressKey(kind string) (opt, contactKind string, ok bool) {
	switch {
	case kind == "email":
		return "to", store.ContactEmail, true
	case strings.HasSuffix(kind, "-sms") || strings.HasSuffix(kind, "-voice"):
		return "phones", store.ContactPhone, true
	}
	return "", "", false
}

func (d *Directory) phoneOwner(v string) *store.Person {
	for i := range d.people {
		for _, ph := range d.people[i].Phones {
			if store.SamePhone(ph, v) {
				return &d.people[i]
			}
		}
	}
	return nil
}

// Owner is the person an address belongs to (nil when unknown).
func (d *Directory) Owner(kind, v string) *store.Person { return d.owner(kind, v) }

// ByHandle is the person with this handle (nil when unknown).
func (d *Directory) ByHandle(h string) *store.Person {
	return d.byHandle[strings.ToLower(strings.TrimPrefix(h, "@"))]
}

// SplitList splits a recipient list into its items.
func SplitList(s string) []string { return splitList(s) }

func (d *Directory) owner(kind, v string) *store.Person {
	if kind == store.ContactPhone {
		return d.phoneOwner(v)
	}
	return d.email[store.NormalizeEmail(v)]
}

// TakesReport keeps the addresses whose owner did not switch the daily
// status report off.
func (d *Directory) TakesReport(kind string, send []string) []string {
	out := []string{}
	for _, v := range send {
		if p := d.owner(kind, v); p == nil || !p.NoReport {
			out = append(out, v)
		}
	}
	return out
}

func (d *Directory) bounce(kind, v string) (store.BounceInfo, bool) {
	key := v
	if kind == store.ContactPhone {
		key = store.NormalizePhone(v)
		if b, ok := d.bounces[kind+"|"+key]; ok {
			return b, true
		}
		for k, b := range d.bounces {
			if strings.HasPrefix(k, kind+"|") && store.SamePhone(b.Address, v) {
				return b, true
			}
		}
		return store.BounceInfo{}, false
	}
	b, ok := d.bounces[kind+"|"+store.NormalizeEmail(key)]
	return b, ok
}

// Resolve turns a channel's recipient list (addresses and @handles,
// comma separated) into the addresses to send to and the issues found.
func (d *Directory) Resolve(kind, list string) (send []string, issues []Issue) {
	seen := map[string]bool{}
	add := func(v string) {
		k := strings.ToLower(v)
		if kind == store.ContactPhone {
			k = store.NormalizePhone(v)
		}
		if !seen[k] {
			seen[k] = true
			send = append(send, v)
		}
	}
	check := func(written, v string, p *store.Person) {
		if p == nil {
			p = d.owner(kind, v)
		}
		handle := ""
		if p != nil {
			handle = p.Handle
		}
		switch {
		case p == nil:
			issues = append(issues, Issue{Code: IssueUnknown, Address: written})
		case p.Left():
			issues = append(issues, Issue{Code: IssueLeft, Address: written, Person: handle})
		case !p.Active():
			issues = append(issues, Issue{Code: IssueInactive, Address: written, Person: handle})
		default:
			if b, ok := d.bounce(kind, v); ok && d.threshold > 0 && b.Count >= d.threshold {
				issues = append(issues, Issue{Code: IssueBouncing, Address: written, Person: handle, Count: b.Count})
				return
			}
			add(v)
		}
	}
	for _, item := range splitList(list) {
		if h, ok := strings.CutPrefix(item, "@"); ok {
			p := d.byHandle[strings.ToLower(h)]
			if p == nil {
				issues = append(issues, Issue{Code: IssueUnknownHandle, Address: item})
				continue
			}
			addrs := p.Emails
			if kind == store.ContactPhone {
				addrs = p.Phones
			}
			if len(addrs) == 0 {
				issues = append(issues, Issue{Code: IssueNoAddress, Address: item, Person: p.Handle})
				continue
			}
			for _, a := range addrs {
				check(item, a, p)
			}
			continue
		}
		check(item, item, nil)
	}
	if len(send) == 0 && len(splitList(list)) > 0 {
		issues = append(issues, Issue{Code: IssueNoRecipients})
	}
	sort.SliceStable(issues, func(i, j int) bool { return rank[issues[i].Code] < rank[issues[j].Code] })
	return send, issues
}

var rank = map[string]int{IssueUnknownHandle: 0, IssueNoRecipients: 1, IssueLeft: 2, IssueInactive: 3, IssueBouncing: 4, IssueNoAddress: 5, IssueUnknown: 6}

// splitList splits "a@x, Name <b@y>, @ops" on commas outside angle
// brackets and quotes.
func splitList(s string) []string {
	var out []string
	var cur strings.Builder
	depth, quoted := 0, false
	flush := func() {
		if v := strings.TrimSpace(cur.String()); v != "" {
			out = append(out, v)
		}
		cur.Reset()
	}
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
		case r == '<' && !quoted:
			depth++
		case r == '>' && !quoted && depth > 0:
			depth--
		case r == ',' && depth == 0 && !quoted:
			flush()
			continue
		}
		cur.WriteRune(r)
	}
	flush()
	return out
}

// Unknown lists the addresses of the given kind that the directory does
// not know, from a recipient list (for the "add to the directory" prompt).
func (d *Directory) Unknown(kind, list string) []string {
	var out []string
	for _, item := range splitList(list) {
		if strings.HasPrefix(item, "@") {
			continue
		}
		if d.owner(kind, item) == nil {
			out = append(out, item)
		}
	}
	return out
}
