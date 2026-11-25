package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// DOCKER checks a container on the probing node through the Docker Engine
// API: it must be running and, when it has a health check, healthy.

var dockerOpts = map[string]bool{"socket": true}

const defaultDockerSocket = "/var/run/docker.sock"

func validateDocker(target string, o opts) error {
	if err := rejectUnknown(o, dockerOpts); err != nil {
		return err
	}
	if target == "" || strings.ContainsAny(target, "/?# ") {
		return fmt.Errorf("DOCKER target is a container name or ID")
	}
	return nil
}

type dockerState struct {
	State struct {
		Status     string
		Running    bool
		Restarting bool
		ExitCode   int
		StartedAt  string
		Health     *struct {
			Status string
			Log    []struct{ Output string }
		}
	}
	Config struct{ Image string }
}

func runDocker(ctx context.Context, s Spec) Result {
	if runtime.GOOS == "windows" {
		return abstain(ReasonPlatform, "Docker checks need the Docker socket of a Linux or macOS node")
	}
	o := opts(s.Opts)
	sock := o.str("socket", defaultDockerSocket)
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}
	defer tr.CloseIdleConnections()
	start := time.Now()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/"+url.PathEscape(s.Target)+"/json", nil)
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOENT):
			return abstain(ReasonPlatform, "no Docker socket at %s on this node", sock)
		case errors.Is(err, fs.ErrPermission), errors.Is(err, syscall.EACCES):
			return abstain(ReasonPrivilege, "no permission to read %s (add the user to the docker group)", sock)
		}
		return abstain(ReasonPlatform, "Docker API: %v", rootErr(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	r := Result{Latency: time.Since(start), Evidence: &Evidence{Peer: sock}}
	r.Metrics = map[string]float64{"latency_ms": ms(r.Latency)}
	if resp.StatusCode == http.StatusNotFound {
		return failKeep(r, ReasonAssertFailed, "no container named %s", s.Target)
	}
	if resp.StatusCode != http.StatusOK {
		return failKeep(r, ReasonProtoError, "Docker API answered HTTP %d", resp.StatusCode)
	}
	var st dockerState
	if err := json.Unmarshal(body, &st); err != nil {
		return failKeep(r, ReasonProtoError, "Docker API: %v", err)
	}
	switch {
	case st.State.Restarting:
		return failKeep(r, ReasonAssertFailed, "container is restarting (last exit code %d)", st.State.ExitCode)
	case !st.State.Running:
		return failKeep(r, ReasonAssertFailed, "container is %s (exit code %d)", st.State.Status, st.State.ExitCode)
	}
	if h := st.State.Health; h != nil {
		switch h.Status {
		case "unhealthy":
			last := ""
			if n := len(h.Log); n > 0 {
				last = ": " + strings.TrimSpace(h.Log[n-1].Output)
			}
			return failKeep(r, ReasonAssertFailed, "container is unhealthy%s", last)
		case "starting":
			r.Vote, r.Reason, r.Detail = VoteOK, ReasonOK, "container running, health check starting"
			return r
		}
	}
	r.Vote, r.Reason = VoteOK, ReasonOK
	r.Detail = "container running"
	if st.State.Health != nil {
		r.Detail = "container running and healthy"
	}
	if st.Config.Image != "" {
		r.Detail += " (" + st.Config.Image + ")"
	}
	return r
}
