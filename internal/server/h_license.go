package server

import (
	"github.com/githubflyideas/unagikuma/internal/app"
	"io"
	"net/http"

	"github.com/githubflyideas/unagikuma/internal/license"
)

// CodeRegistration means the feature needs a registered installation.
const CodeRegistration = "registration_required"

// registrationError is the refusal a registered-only feature serves.
func registrationError(s *Server) error {
	st := s.app.License().State()
	e := Errorf(http.StatusPaymentRequired, CodeRegistration, "this feature needs a registered installation; to register, please contact the author (Settings › Registration)")
	e.Extra = map[string]any{"installation_id": st.InstallationID}
	return e
}

// requireRegistered serves a registered-only feature, or says what is needed.
func requireRegistered(s *Server, f apiFunc) apiFunc {
	return func(w http.ResponseWriter, r *http.Request) (any, error) {
		if !s.app.License().State().Registered {
			return nil, registrationError(s)
		}
		return f(w, r)
	}
}

// CodeQuota means the licence does not allow one more node, account or
// monitor.
const CodeQuota = "quota_exceeded"

func quotaError(err error) error {
	return Errorf(http.StatusPaymentRequired, CodeQuota, "%v", err)
}

func (s *Server) licenseGet(w http.ResponseWriter, r *http.Request) (any, error) {
	st := s.app.License().State()
	cap := s.app.MonitorCap()
	return struct {
		license.State
		Usage        app.Usage `json:"usage"`
		UserLimit    int       `json:"user_limit"`
		MonitorLimit int       `json:"monitor_limit"`
		NodeLimit    int       `json:"node_limit"`
		FreeUsers    int       `json:"free_users"`
		FreeMonitors int       `json:"free_monitors"`
		FreeNodes    int       `json:"free_nodes"`
		// In a cluster: the shared configuration's free number, and each
		// site's own; this site's own monitors and their limit.
		FreeShared   int `json:"free_shared_monitors"`
		FreeSite     int `json:"free_site_monitors"`
		SiteMonitors int `json:"site_monitors"`
		SiteLimit    int `json:"site_monitor_limit,omitempty"`
	}{st, s.app.Usage(), app.UserLimit(st), cap.Limit, app.NodeLimit(st), license.FreeUsers, license.FreeMonitors, license.FreeNodes,
		license.FreeShared, license.FreeSite, cap.SiteUsed, cap.SiteLimit}, nil
}

// licensePut installs a licence file (its JSON as the body); an invalid
// one is refused and the current one kept.
func (s *Server) licensePut(w http.ResponseWriter, r *http.Request) (any, error) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		return nil, Errorf(http.StatusRequestEntityTooLarge, CodeTooLarge, "a licence is a small JSON file")
	}
	st, err := s.app.License().Install(data, s.now())
	if err != nil {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err)
	}
	auditOf(r).note("customer", st.Customer).note("fingerprint", st.Fingerprint).target = license.FileName
	s.app.LicenseChanged()
	return st, nil
}

func (s *Server) licenseDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	return s.app.License().Remove(s.now())
}
