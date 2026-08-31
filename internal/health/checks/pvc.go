package checks

import (
	"context"
	"fmt"

	"github.com/jthunderbird/kelper/internal/health"
	corev1 "k8s.io/api/core/v1"
)

// Volumes reports PersistentVolumeClaims that are filling up or unbound. Volume
// utilization is not available from metrics-server; it comes from the kubelet
// summary API, so the usage portion requires node proxy access.
type Volumes struct {
	// Threshold is the fill percentage at which a volume is flagged. Defaults
	// to 85.
	Threshold float64
}

// Name identifies the check.
func (Volumes) Name() string { return "volumes" }

// Description is one line of help text.
func (Volumes) Description() string {
	return "PersistentVolumeClaims near capacity, out of inodes, or stuck unbound"
}

// Requires reports no capabilities: the unbound-PVC portion works everywhere,
// and the usage portion is skipped when volume stats are absent.
func (Volumes) Requires() []health.Capability { return nil }

func (c Volumes) threshold() float64 {
	if c.Threshold > 0 {
		return c.Threshold
	}
	return 85
}

// Run evaluates PVC binding state and, where available, usage.
func (c Volumes) Run(ctx context.Context, s *health.Snapshot) ([]health.Finding, error) {
	var out []health.Finding

	for i := range s.PVCs {
		pvc := &s.PVCs[i]
		ref := health.ResourceRef{
			Kind: "PersistentVolumeClaim", Namespace: pvc.Namespace, Name: pvc.Name,
		}

		switch pvc.Status.Phase {
		case corev1.ClaimPending:
			out = append(out, health.Finding{
				Severity:    health.SeverityError,
				Ref:         ref,
				Reason:      "PVCPending",
				Message:     "claim is unbound; pods using it cannot start",
				Remediation: "Check the StorageClass exists and its provisioner is running",
				Evidence:    evidenceFromEvents(s, pvc.UID, 2),
			})
			continue
		case corev1.ClaimLost:
			out = append(out, health.Finding{
				Severity:    health.SeverityCritical,
				Ref:         ref,
				Reason:      "PVCLost",
				Message:     "claim has lost its underlying PersistentVolume",
				Remediation: "The backing volume is gone; restore from backup",
			})
			continue
		}

		stat, ok := s.VolumeStats[pvc.Namespace+"/"+pvc.Name]
		if !ok {
			continue
		}
		ref.Node = stat.Node

		if pct := stat.UsedPercent(); pct >= c.threshold() {
			out = append(out, health.Finding{
				Severity: severityForPercent(pct),
				Ref:      ref,
				Reason:   "VolumeNearCapacity",
				Message: fmt.Sprintf("volume is %.0f%% full (%s of %s)",
					pct, humanBytes(stat.UsedBytes), humanBytes(stat.CapacityBytes)),
				Remediation: "Expand the claim if its StorageClass allows it, or free space in the volume",
				Evidence: []health.Evidence{
					{Label: "used", Value: humanBytes(stat.UsedBytes)},
					{Label: "capacity", Value: humanBytes(stat.CapacityBytes)},
					{Label: "node", Value: stat.Node},
				},
			})
		}

		// Inode exhaustion fills no space but breaks writes just as hard, and
		// is easy to miss because free bytes still look healthy.
		if total := stat.InodesUsed + stat.InodesFree; total > 0 {
			if pct := float64(stat.InodesUsed) / float64(total) * 100; pct >= c.threshold() {
				out = append(out, health.Finding{
					Severity: severityForPercent(pct),
					Ref:      ref,
					Reason:   "VolumeInodesLow",
					Message: fmt.Sprintf("volume has used %.0f%% of its inodes (%d of %d)",
						pct, stat.InodesUsed, total),
					Detail:      "Writes fail once inodes are exhausted even when free space remains.",
					Remediation: "Remove small files, or recreate the volume with a higher inode ratio",
				})
			}
		}
	}
	return out, nil
}
