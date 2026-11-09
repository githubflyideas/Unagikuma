package server

import (
	"github.com/githubflyideas/unagikuma/internal/app"
	"io"
	"net/http"

	"github.com/githubflyideas/unagikuma/internal/license"
)

// CodeRegistration means the feature needs a registered installation.
const CodeRegistration = "registration_required"

// requireFeature serves a registered-only feature, or says what is needed.
func requireFeature(s *Server, feature string, f apiFunc) apiFunc {
	return func(w http.ResponseWriter, r *http.Request) (any, error) {
		if st := s.app.License().State(); !st.Allowed(feature) {
			e := Errorf(http.StatusPaymentRequired, CodeRegistration, "this feature needs a registered installation (Settings › Registration)")
			e.Extra = map[string]any{"feature": feature, "installation_id": st.InstallationID}
			return nil, e
		}
		return f(w, r)
	}
}

// CodeQuota means the licence does not allow one more node or account.
const CodeQuota = "quota_exceeded"

func quotaError(err error) error {
	return Errorf(http.StatusPaymentRequired, CodeQuota, "%v", err)
}

func (s *Server) licenseGet(w http.ResponseWriter, r *http.Request) (any, error) {
	return struct {
		license.State
		Usage app.Usage `json:"usage"`
	}{s.app.License().State(), s.app.Usage()}, nil
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
	auditOf(r).note("customer", st.Customer).note("tier", st.Tier).note("fingerprint", st.Fingerprint).target = license.FileName
	return st, nil
}

func (s *Server) licenseDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	return s.app.License().Remove(s.now())
}
