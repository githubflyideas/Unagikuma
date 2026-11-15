package probe

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/appprofile"
)

// An APP monitor is the HTTP check with app=<profile>: the profile says
// where the application's status page is and what each number on it is,
// and the numbers become the metrics the value rules judge. field= names
// the number judged (the profile's primary one when it is not set).

// appOpts are the options of an APP monitor: the application's page and
// how it is fetched, then the value rules.
var appOpts = []string{"app", "field", "select", "expect", "host", "basic", "bearer", "proxy", "insecure", "user-agent",
	"cert-days", "cert-critical-days", "warn", "crit", "change", "compare", "min", "for", "direction"}

// appProfile is the profile a spec names, if it names one.
func appProfile(o opts) (*appprofile.Profile, bool) {
	if !o.has("app") {
		return nil, false
	}
	return appprofile.App(o.str("app", ""))
}

// appField is the field the monitor judges.
func appField(p *appprofile.Profile, o opts) (*appprofile.Field, bool) {
	return p.Field(o.str("field", p.Primary))
}

// appUnit is the unit of the number an APP monitor judges.
func appUnit(o opts) string {
	if p, ok := appProfile(o); ok {
		if f, ok := appField(p, o); ok {
			return f.Unit
		}
	}
	return ""
}

func validateApp(o opts) error {
	if !o.has("app") {
		for _, k := range []string{"field", "select"} {
			if o.has(k) {
				return fmt.Errorf("%s= goes with app=", k)
			}
		}
		return nil
	}
	p, ok := appProfile(o)
	if !ok {
		return fmt.Errorf("unknown app %q (%s)", o.str("app", ""), strings.Join(appprofile.IDs(), ", "))
	}
	if o.has("json-path") || o.has("keyword") || o.has("keyword-regex") || o.has("absent") {
		return errors.New("app= reads the page itself: it cannot be combined with json-path=, keyword=, keyword-regex= or absent=")
	}
	if o.has("field") {
		if _, ok := p.Field(o.str("field", "")); !ok {
			keys := make([]string, 0, len(p.Fields))
			for _, f := range p.Fields {
				keys = append(keys, f.Key)
			}
			sort.Strings(keys)
			return fmt.Errorf("%s has no field %q (%s)", p.Title, o.str("field", ""), strings.Join(keys, ", "))
		}
	}
	if o.has("select") {
		if p.Reader != "metrics" {
			return fmt.Errorf("select= narrows a metrics page; the %s page is not one", p.Title)
		}
		if _, err := appprofile.ParseSelect(o.str("select", "")); err != nil {
			return err
		}
	}
	return nil
}

// appAssertions reads the page with the profile. A page that is not the
// application's fails with what is missing; the numbers go into the
// metrics ("app.<field>"), and the judged one is "value" (flagged as a
// counter when it only grows, so the rules judge its rate).
func appAssertions(r Result, o opts, data []byte) Result {
	p, _ := appProfile(o)
	var sel appprofile.Select
	if o.has("select") {
		sel, _ = appprofile.ParseSelect(o.str("select", ""))
	}
	vals, err := p.Read(data, sel)
	if err != nil {
		return failKeep(r, ReasonAssertFailed, "%s", err.Error())
	}
	f, _ := appField(p, o)
	v, ok := vals[f.Key]
	if !ok {
		return failKeep(r, ReasonAssertFailed, "%s has no %q on this page (%s)", p.Title, f.Label, o.str("select", "no select"))
	}
	if r.Metrics == nil {
		r.Metrics = map[string]float64{}
	}
	for k, x := range vals {
		r.Metrics["app."+k] = x
	}
	r.Metrics["value"] = v
	if f.Type == "counter" {
		r.Metrics["value.counter"] = 1
	}
	r.Detail = p.Describe(vals)
	return r
}
