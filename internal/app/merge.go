package app

import (
	"context"
	"net/http"
	"time"

	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// mergeEvery is how often a connected node sends the master what it wrote
// locally, independently of its configuration authority role.
const mergeEvery = time.Minute

// maybePushMerge starts a merge push when one is due: right after a
// replica connects, then every mergeEvery. Local checks never hand back.
func (a *App) maybePushMerge(now bool) {
	a.am.mu.Lock()
	ag := a.am.agent
	a.am.mu.Unlock()
	if ag == nil || !ag.Status().Connected {
		return
	}
	r := &a.replica
	r.mu.Lock()
	if r.merging || a.now().Sub(r.merged) < 15*time.Second || (!now && a.now().Sub(r.merged) < mergeEvery) {
		r.mu.Unlock()
		return
	}
	r.merging = true
	r.mu.Unlock()
	go func() {
		err := a.pushMerge(ag)
		r.mu.Lock()
		r.merging = false
		r.merged = a.now()
		if err != nil {
			r.merged = r.merged.Add(15*time.Second - mergeEvery) // try again soon, not every round
		}
		r.mu.Unlock()
		if err != nil {
			a.log.Warn("sending this site's records to the master", "err", err)
		}
	}()
}

// pushMerge sends every record above the master's marks, batch by batch.
func (a *App) pushMerge(ag *cluster.Agent) error {
	if err := a.pushDiverged(ag); err != nil {
		a.log.Warn("sending the diverged configuration to the master", "err", err)
	}
	epoch, err := a.store.DBEpoch()
	if err != nil {
		return err
	}
	total := map[string]int{}
	for i := 0; i < 1000; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		var marks map[string]int64
		err := ag.Call(ctx, http.MethodGet, cluster.MergePath+"?epoch="+epoch, nil, &marks)
		if err != nil {
			cancel()
			return err
		}
		tables, more, err := a.store.ExportSince(marks)
		if err != nil || len(tables) == 0 {
			cancel()
			if len(total) > 0 {
				a.log.Info("records of this site merged into the master", "added", total)
			}
			return err
		}
		var res store.MergeResult
		err = ag.Call(ctx, http.MethodPost, cluster.MergePath, store.MergeBatch{Epoch: epoch, Term: a.Term(), Tables: tables}, &res)
		cancel()
		if err != nil {
			return err
		}
		for k, v := range res.Added {
			total[k] += v
		}
		if !more && len(res.Added) == 0 {
			return nil
		}
	}
	return nil
}

// MergeMarks are the master's marks for a site.
func (a *App) MergeMarks(site string, epoch ...string) (map[string]int64, error) {
	e := ""
	if len(epoch) > 0 {
		e = epoch[0]
	}
	return a.store.MergeMarks(store.MergeScope(site, e))
}

// MergeFrom adds a site's records (on the master).
func (a *App) MergeFrom(site string, b store.MergeBatch) (store.MergeResult, error) {
	b.Site = site
	res, err := a.store.Merge(b)
	if err == nil && len(res.Added) > 0 {
		_ = a.store.Audit(auditEntry("cluster.merge", "system", map[string]any{"site": site, "term": b.Term, "added": res.Added}))
	}
	return res, err
}
