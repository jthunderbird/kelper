package pull

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	utilexec "k8s.io/client-go/util/exec"
	"k8s.io/utils/ptr"
)

const keycloakDigest = "09a381c715ab0b111835b70f2905955274843a219c6f27efb348e4d9f4086858"

func testPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps"},
		Spec: corev1.PodSpec{
			ServiceAccountName: "web-sa",
			ImagePullSecrets:   []corev1.LocalObjectReference{{Name: "pod-secret"}},
			Containers: []corev1.Container{
				{Name: "app", Image: "quay.io/keycloak/keycloak:26.0"},
				{Name: "sidecar", Image: "busybox:1.37"},
			},
		},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "app", ImageID: "quay.io/keycloak/keycloak@sha256:" + keycloakDigest,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		}},
	}
}

func TestSelectContainer(t *testing.T) {
	pod := testPod()
	if c, _ := selectContainer(pod, ""); c.Name != "app" {
		t.Errorf("default = %q, want first container", c.Name)
	}
	pod.Annotations = map[string]string{defaultContainerAnnotation: "sidecar"}
	if c, _ := selectContainer(pod, ""); c.Name != "sidecar" {
		t.Errorf("default = %q, want annotated container", c.Name)
	}
	if c, _ := selectContainer(pod, "app"); c.Name != "app" {
		t.Errorf("-c app = %q", c.Name)
	}
	if _, err := selectContainer(pod, "missing"); err == nil {
		t.Error("expected an error for an unknown container")
	}
}

func TestParseImageID(t *testing.T) {
	want := "quay.io/keycloak/keycloak@sha256:" + keycloakDigest
	for _, id := range []string{
		want,
		"docker-pullable://" + want,
	} {
		ref, digest, err := parseImageID(id)
		if err != nil || ref != want || digest != keycloakDigest {
			t.Errorf("parseImageID(%q) = (%q, %q, %v)", id, ref, digest, err)
		}
	}
	for _, id := range []string{
		"sha256:" + keycloakDigest,
		"docker://sha256:" + keycloakDigest,
	} {
		if _, _, err := parseImageID(id); !errors.Is(err, ErrSideLoaded) {
			t.Errorf("parseImageID(%q) error = %v, want ErrSideLoaded", id, err)
		}
	}
}

func TestResolveTarget(t *testing.T) {
	cs := fake.NewSimpleClientset(testPod())
	tgt, err := resolveTarget(context.Background(), cs, "apps", "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Ref != "quay.io/keycloak/keycloak@sha256:"+keycloakDigest || !tgt.Running {
		t.Errorf("target = %+v", tgt)
	}
	if _, err := resolveTarget(context.Background(), cs, "apps", "web", "sidecar"); err == nil {
		t.Error("expected an error for a container with no status yet")
	}
}

func TestArchiveTagAndOutput(t *testing.T) {
	tgt := &target{Ref: "quay.io/keycloak/keycloak@sha256:" + keycloakDigest, Digest: keycloakDigest,
		Container: corev1.Container{Image: "quay.io/keycloak/keycloak:26.0"}}
	if got := archiveTag(tgt); got != "quay.io/keycloak/keycloak:26.0" {
		t.Errorf("archiveTag = %q", got)
	}
	if got := defaultOutput(tgt); got != "keycloak_09a381c715ab.tar" {
		t.Errorf("defaultOutput = %q", got)
	}

	tgt.Container.Image = "registry.local:5000/team/app@sha256:" + keycloakDigest
	tgt.Ref = tgt.Container.Image
	if got := archiveTag(tgt); got != "registry.local:5000/team/app:09a381c715ab" {
		t.Errorf("archiveTag for a pinned image = %q", got)
	}
	tgt.Container.Image = "registry.local:5000/team/app"
	if got := archiveTag(tgt); got != "registry.local:5000/team/app:09a381c715ab" {
		t.Errorf("archiveTag for an untagged image = %q (the port is not a tag)", got)
	}
}

func TestCollectAuths(t *testing.T) {
	secret := func(name, key, data string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "apps"},
			Data: map[string][]byte{key: []byte(data)}}
	}
	cs := fake.NewSimpleClientset(
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "web-sa", Namespace: "apps"},
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: "sa-secret"}, {Name: "missing"}}},
		secret("pod-secret", corev1.DockerConfigJsonKey, `{"auths":{"quay.io":{"username":"pod"}}}`),
		secret("sa-secret", corev1.DockerConfigKey, `{"quay.io":{"username":"sa"},"ghcr.io":{"username":"sa"}}`),
	)

	var warnings []string
	auths := collectAuths(context.Background(), cs, testPod(), func(f string, a ...any) { warnings = append(warnings, f) })
	if auths["quay.io"].Username != "pod" {
		t.Errorf("quay.io user = %q, want the pod's secret to take precedence", auths["quay.io"].Username)
	}
	if auths["ghcr.io"].Username != "sa" {
		t.Errorf("ghcr.io user = %q, want the service account's secret", auths["ghcr.io"].Username)
	}
	if len(warnings) != 1 {
		t.Errorf("warnings = %v, want one for the missing secret", warnings)
	}
}

func TestCarrierContainer(t *testing.T) {
	tgt := &target{Pod: testPod(), Container: testPod().Spec.Containers[0]}
	ec := carrierContainer(tgt, "kelper-pull-abc123", "quay.io/keycloak/keycloak:26.0", 630)

	if ec.ImagePullPolicy != corev1.PullIfNotPresent {
		t.Errorf("pull policy = %q, want IfNotPresent so :latest does not pull", ec.ImagePullPolicy)
	}
	script := ec.Command[2]
	for _, want := range []string{"trap", "TERM", "sleep 630 & wait", "rm -f " + fetcherPath} {
		if !strings.Contains(script, want) {
			t.Errorf("carrier script %q is missing %q", script, want)
		}
	}
	sc := ec.SecurityContext
	if !*sc.RunAsNonRoot || *sc.RunAsUser != fallbackUID || *sc.AllowPrivilegeEscalation ||
		sc.Capabilities.Drop[0] != "ALL" || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("security context = %+v", sc)
	}
}

func TestRunAsIDsPrefersTarget(t *testing.T) {
	tgt := &target{Pod: testPod(), Container: testPod().Spec.Containers[0]}
	tgt.Pod.Spec.SecurityContext = &corev1.PodSecurityContext{RunAsUser: ptr.To[int64](1000), RunAsGroup: ptr.To[int64](2000)}
	if uid, gid := runAsIDs(tgt); uid != 1000 || gid != 2000 {
		t.Errorf("pod-level ids = %d:%d", uid, gid)
	}
	tgt.Container.SecurityContext = &corev1.SecurityContext{RunAsUser: ptr.To[int64](3000)}
	if uid, gid := runAsIDs(tgt); uid != 3000 || gid != 2000 {
		t.Errorf("container-level ids = %d:%d", uid, gid)
	}
	// A root target must not make the carrier root.
	tgt.Container.SecurityContext = &corev1.SecurityContext{RunAsUser: ptr.To[int64](0)}
	if uid, _ := runAsIDs(tgt); uid == 0 {
		t.Error("carrier must not run as root")
	}
}

func TestCarrierState(t *testing.T) {
	pod := func(state corev1.ContainerState) *corev1.Pod {
		return &corev1.Pod{Status: corev1.PodStatus{EphemeralContainerStatuses: []corev1.ContainerStatus{
			{Name: "kp", State: state}}}}
	}
	if err := carrierState(&corev1.Pod{}, "kp"); err != errNotYet {
		t.Errorf("no status: %v", err)
	}
	if err := carrierState(pod(corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}), "kp"); err != errNotYet {
		t.Errorf("creating: %v", err)
	}
	if err := carrierState(pod(corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}), "kp"); err != nil {
		t.Errorf("running: %v", err)
	}
	err := carrierState(pod(corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull", Message: "401"}}), "kp")
	if err == nil || !strings.Contains(err.Error(), "--helper-image") {
		t.Errorf("pull failure: %v", err)
	}
}

func TestConfirm(t *testing.T) {
	tgt := &target{Pod: testPod()}
	for answer, want := range map[string]bool{"y\n": true, "Y\n": true, "n\n": false, "\n": false, "": false} {
		var out bytes.Buffer
		ok, err := confirm(strings.NewReader(answer), &out, tgt, "kelper-pull-x", "img", time.Minute)
		if err != nil || ok != want {
			t.Errorf("answer %q: got (%v, %v), want %v", answer, ok, err, want)
		}
		if !strings.Contains(out.String(), "stays listed on the pod") {
			t.Errorf("prompt does not explain the lasting change: %q", out.String())
		}
	}
}

func TestToolsMissing(t *testing.T) {
	if !toolsMissing(utilexec.CodeExitError{Err: errors.New("exit 1"), Code: 1}) {
		t.Error("a non-zero probe exit means a tool is missing")
	}
	if !toolsMissing(errors.New(`OCI runtime exec failed: exec: "sh": executable file not found in $PATH`)) {
		t.Error("a missing shell means the tools are missing")
	}
	if toolsMissing(errors.New("unable to upgrade connection: pods \"web\" is forbidden")) {
		t.Error("an RBAC failure must not be reported as missing tools")
	}
}
