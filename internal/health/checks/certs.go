package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/jthunderbird/kelper/internal/health"
	certsv1 "k8s.io/api/certificates/v1"
)

const (
	// certWarnBefore is when an upcoming expiry becomes a warning.
	certWarnBefore = 30 * 24 * time.Hour
	// certErrorBefore is when it becomes urgent.
	certErrorBefore = 7 * 24 * time.Hour
)

// Certificates reports expiring cluster certificates and CSRs awaiting
// approval. Expiry is read by TLS handshake rather than kubelet metrics, since
// the certificate-manager gauges are absent on distributions that manage certs
// as files.
type Certificates struct{}

// Name identifies the check.
func (Certificates) Name() string { return "certs" }

// Description is one line of help text.
func (Certificates) Description() string {
	return "Expiring API server, kubelet and client certificates, and pending CSRs"
}

// Requires reports no capabilities: handshakes need only network reach.
func (Certificates) Requires() []health.Capability { return nil }

// Run evaluates collected certificate expiries and pending CSRs.
func (c Certificates) Run(ctx context.Context, s *health.Snapshot) ([]health.Finding, error) {
	var out []health.Finding

	for endpoint, notAfter := range s.CertExpiry {
		remaining := time.Until(notAfter)
		sev, report := certSeverity(remaining)
		if !report {
			continue
		}
		msg := fmt.Sprintf("certificate expires in %s (%s)",
			roundDur(remaining), notAfter.Format(time.RFC3339))
		if remaining < 0 {
			msg = fmt.Sprintf("certificate EXPIRED %s ago (%s)",
				roundDur(-remaining), notAfter.Format(time.RFC3339))
		}
		out = append(out, health.Finding{
			Severity:    sev,
			Ref:         health.ResourceRef{Kind: "Certificate", Name: endpoint},
			Reason:      "CertificateExpiring",
			Message:     msg,
			Remediation: "Rotate the certificate before it lapses; an expired one locks the cluster out",
			Evidence: []health.Evidence{
				{Label: "endpoint", Value: endpoint},
				{Label: "expires", Value: notAfter.Format(time.RFC3339), Time: notAfter},
			},
		})
	}

	for i := range s.CSRs {
		csr := &s.CSRs[i]
		if csrDecided(csr) {
			continue
		}
		out = append(out, health.Finding{
			Severity: health.SeverityWarn,
			Ref:      health.ResourceRef{Group: "certificates.k8s.io", Kind: "CertificateSigningRequest", Name: csr.Name},
			Reason:   "CSRPending",
			Message: fmt.Sprintf("CSR from %q has been pending for %s",
				csr.Spec.Username, ageOf(csr.CreationTimestamp.Time)),
			Detail:      "signer: " + csr.Spec.SignerName,
			Remediation: "kubectl certificate approve " + csr.Name,
			Since:       csr.CreationTimestamp.Time,
		})
	}
	return out, nil
}

// certSeverity maps remaining lifetime to a severity, reporting nothing while
// the certificate has comfortable headroom.
func certSeverity(remaining time.Duration) (health.Severity, bool) {
	switch {
	case remaining < 0:
		return health.SeverityCritical, true
	case remaining < certErrorBefore:
		return health.SeverityError, true
	case remaining < certWarnBefore:
		return health.SeverityWarn, true
	default:
		return health.SeverityOk, false
	}
}

// csrDecided reports whether a CSR has been approved or denied.
func csrDecided(csr *certsv1.CertificateSigningRequest) bool {
	for _, cond := range csr.Status.Conditions {
		if cond.Type == certsv1.CertificateApproved || cond.Type == certsv1.CertificateDenied {
			return true
		}
	}
	return false
}
