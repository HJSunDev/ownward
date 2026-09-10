package contract

import (
	"errors"
	"net/url"
	"time"
)

const AccessControlStateSchema = "ownward.control-state/v3"

type Location struct {
	ServiceID   string `json:"service_id"`
	SystemID    string `json:"system_id,omitempty"`
	Endpoint    string `json:"endpoint"`
	Certificate string `json:"certificate"`
	Composition string `json:"composition"`
}

func (l Location) Validate() error {
	u, err := url.Parse(l.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || l.ServiceID == "" || l.Certificate == "" || l.Composition == "" {
		return errors.New("受保护连接描述无效")
	}
	return nil
}

type Enrollment struct {
	ID               string       `json:"id"`
	Manager          string       `json:"manager"`
	Name             string       `json:"name,omitempty"`
	ProofDigest      string       `json:"proof_digest,omitempty"`
	Permissions      []Permission `json:"permissions,omitempty"`
	Principal        string       `json:"principal,omitempty"`
	Status           string       `json:"status"`
	Expires          time.Time    `json:"expires"`
	Claimed          bool         `json:"claimed,omitempty"`
	ApproverRevision uint64       `json:"approver_revision,omitempty"`
}

type Handoff struct {
	Cleaned       bool     `json:"cleaned,omitempty"`
	ID            string   `json:"id"`
	Target        Location `json:"target"`
	Phase         string   `json:"phase"`
	Snapshot      string   `json:"snapshot,omitempty"`
	Revision      uint64   `json:"revision"`
	LocationSaved bool     `json:"location_saved"`
}

type AccessState struct {
	Enrollments []Enrollment `json:"enrollments,omitempty"`
	Handoff     *Handoff     `json:"handoff,omitempty"`
	Cancelled   []Handoff    `json:"cancelled,omitempty"`
}

func (s AccessState) Validate() error {
	if len(s.Enrollments) > 256 {
		return errors.New("待接入操作过多")
	}
	seen := map[string]bool{}
	for _, e := range s.Enrollments {
		if e.ID == "" || seen[e.ID] || e.Manager == "" || e.Expires.IsZero() {
			return errors.New("接入操作无效")
		}
		seen[e.ID] = true
		switch e.Status {
		case "waiting", "pending", "approved", "declined", "cancelled":
		default:
			return errors.New("接入状态无效")
		}
	}
	if h := s.Handoff; h != nil {
		if h.ID == "" || h.Revision == 0 || h.Target.Validate() != nil {
			return errors.New("交接身份无效")
		}
		switch h.Phase {
		case "prepared", "frozen", "retired", "active":
		default:
			return errors.New("交接状态无效")
		}
		if (h.Phase == "retired" || h.Phase == "active") && (h.Snapshot == "" || !h.LocationSaved) {
			return errors.New("交接尚未就绪")
		}
	}
	return nil
}
