package checks_test

import (
	"context"
	"testing"

	"github.com/jthunderbird/kelper/internal/health"
	"github.com/jthunderbird/kelper/internal/health/checks"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func pvc(name string, phase corev1.PersistentVolumeClaimPhase) corev1.PersistentVolumeClaim {
	return corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: phase},
	}
}

func runVolumes(t *testing.T, threshold float64, mutate func(*health.Snapshot)) []health.Finding {
	t.Helper()
	fs, err := checks.Volumes{Threshold: threshold}.Run(
		context.Background(), health.NewSnapshotForTest(mutate))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return fs
}

func TestPendingPVCIsReported(t *testing.T) {
	fs := runVolumes(t, 85, func(s *health.Snapshot) {
		s.PVCs = []corev1.PersistentVolumeClaim{pvc("unbound", corev1.ClaimPending)}
	})
	if !hasReason(fs, "PVCPending") {
		t.Fatalf("expected PVCPending, got %+v", fs)
	}
}

func TestLostPVCIsCritical(t *testing.T) {
	fs := runVolumes(t, 85, func(s *health.Snapshot) {
		s.PVCs = []corev1.PersistentVolumeClaim{pvc("gone", corev1.ClaimLost)}
	})
	if len(fs) != 1 || fs[0].Severity != health.SeverityCritical {
		t.Fatalf("expected a critical finding, got %+v", fs)
	}
}

// Values mirror what the kubelet summary API reported for a real bound volume
// during live testing: ~45% full.
func TestBoundVolumeBelowThresholdIsQuiet(t *testing.T) {
	fs := runVolumes(t, 85, func(s *health.Snapshot) {
		s.PVCs = []corev1.PersistentVolumeClaim{pvc("data", corev1.ClaimBound)}
		s.VolumeStats["default/data"] = health.VolumeStat{
			Namespace: "default", PVC: "data", Node: "node-1",
			UsedBytes: 16969297920, CapacityBytes: 37518049280,
			InodesUsed: 195114, InodesFree: 18157014,
		}
	})
	if len(fs) != 0 {
		t.Fatalf("expected no findings at 45%% against an 85%% threshold, got %+v", fs)
	}
}

func TestBoundVolumeOverThresholdIsReported(t *testing.T) {
	fs := runVolumes(t, 40, func(s *health.Snapshot) {
		s.PVCs = []corev1.PersistentVolumeClaim{pvc("data", corev1.ClaimBound)}
		s.VolumeStats["default/data"] = health.VolumeStat{
			Namespace: "default", PVC: "data", Node: "node-1",
			UsedBytes: 16969297920, CapacityBytes: 37518049280,
		}
	})
	if !hasReason(fs, "VolumeNearCapacity") {
		t.Fatalf("expected VolumeNearCapacity at 45%% against a 40%% threshold, got %+v", fs)
	}
}

func TestVolumeAboveNinetyFiveIsError(t *testing.T) {
	fs := runVolumes(t, 85, func(s *health.Snapshot) {
		s.PVCs = []corev1.PersistentVolumeClaim{pvc("full", corev1.ClaimBound)}
		s.VolumeStats["default/full"] = health.VolumeStat{
			Namespace: "default", PVC: "full",
			UsedBytes: 98, CapacityBytes: 100,
		}
	})
	if len(fs) == 0 || fs[0].Severity != health.SeverityError {
		t.Fatalf("expected an error-severity finding at 98%%, got %+v", fs)
	}
}

// Inodes can be exhausted while free space still looks healthy.
func TestInodeExhaustionIsReportedIndependently(t *testing.T) {
	fs := runVolumes(t, 85, func(s *health.Snapshot) {
		s.PVCs = []corev1.PersistentVolumeClaim{pvc("many-files", corev1.ClaimBound)}
		s.VolumeStats["default/many-files"] = health.VolumeStat{
			Namespace: "default", PVC: "many-files",
			UsedBytes: 10, CapacityBytes: 100,
			InodesUsed: 95, InodesFree: 5,
		}
	})
	if !hasReason(fs, "VolumeInodesLow") {
		t.Fatalf("expected VolumeInodesLow, got %+v", fs)
	}
	if hasReason(fs, "VolumeNearCapacity") {
		t.Error("space usage is only 10%; it must not be reported")
	}
}

// A bound PVC with no kubelet stats (node proxy unavailable) must stay silent
// rather than reporting 0% usage.
func TestBoundVolumeWithoutStatsIsQuiet(t *testing.T) {
	fs := runVolumes(t, 85, func(s *health.Snapshot) {
		s.PVCs = []corev1.PersistentVolumeClaim{pvc("data", corev1.ClaimBound)}
	})
	if len(fs) != 0 {
		t.Fatalf("expected no findings without volume stats, got %+v", fs)
	}
}
