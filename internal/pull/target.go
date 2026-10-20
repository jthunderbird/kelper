package pull

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/jthunderbird/kelper/internal/pull/fetchproto"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

// defaultContainerAnnotation names the container kubectl picks when -c is
// omitted.
const defaultContainerAnnotation = "kubectl.kubernetes.io/default-container"

// fallbackUID runs the carrier as a non-root user when neither the target
// container nor its pod chooses one. 65532 is the conventional "nonroot" ID.
const fallbackUID int64 = 65532

// ErrSideLoaded reports an image the runtime did not pull from a registry, so
// there is no registry digest to pull it again by.
var ErrSideLoaded = errors.New("image was not pulled from a registry (it was side-loaded onto the node or referenced by image ID); kelper pull cannot fetch it")

// target is the resolved image to pull and where it is running.
type target struct {
	Pod       *corev1.Pod
	Container corev1.Container
	// Running reports whether the container is up, so it can be probed.
	Running bool
	// Ref is the running image pinned by registry digest: repo@sha256:...
	Ref string
	// Digest is the sha256 hex from Ref.
	Digest string
}

// resolveTarget fetches the pod and resolves the container and its running
// image digest.
func resolveTarget(ctx context.Context, cs kubernetes.Interface, namespace, podName, containerName string) (*target, error) {
	pod, err := cs.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("pod %q not found in namespace %q", podName, namespace)
	}
	if err != nil {
		return nil, err
	}
	c, err := selectContainer(pod, containerName)
	if err != nil {
		return nil, err
	}

	var status *corev1.ContainerStatus
	for _, list := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses} {
		for i := range list {
			if list[i].Name == c.Name {
				status = &list[i]
			}
		}
	}
	if status == nil || status.ImageID == "" {
		return nil, fmt.Errorf("container %q has not started yet, so its image digest is unknown", c.Name)
	}

	ref, digest, err := parseImageID(status.ImageID)
	if err != nil {
		return nil, err
	}
	return &target{
		Pod:       pod,
		Container: c,
		Running:   status.State.Running != nil,
		Ref:       ref,
		Digest:    digest,
	}, nil
}

// selectContainer picks the named container, or defaults the way kubectl
// does: the default-container annotation, then the first container.
func selectContainer(pod *corev1.Pod, name string) (corev1.Container, error) {
	if name == "" {
		name = pod.Annotations[defaultContainerAnnotation]
	}
	if name == "" {
		if len(pod.Spec.Containers) == 0 {
			return corev1.Container{}, fmt.Errorf("pod %s has no containers", pod.Name)
		}
		return pod.Spec.Containers[0], nil
	}
	for _, list := range [][]corev1.Container{pod.Spec.Containers, pod.Spec.InitContainers} {
		for _, c := range list {
			if c.Name == name {
				return c, nil
			}
		}
	}
	return corev1.Container{}, fmt.Errorf("container %q not found in pod %s", name, pod.Name)
}

// parseImageID turns a container status imageID into a pullable reference.
// Runtimes report repo@sha256:... for pulled images (cri-dockerd prefixes
// docker-pullable://). A bare sha256:... is the local image ID, which a
// runtime only reports when it knows no registry digest for the image.
func parseImageID(imageID string) (ref, digest string, err error) {
	id := imageID
	for _, prefix := range []string{"docker-pullable://", "docker://"} {
		id = strings.TrimPrefix(id, prefix)
	}
	repo, hexDigest, ok := strings.Cut(id, "@sha256:")
	if !ok || repo == "" {
		return "", "", ErrSideLoaded
	}
	if len(hexDigest) != 64 {
		return "", "", fmt.Errorf("unexpected image ID %q", imageID)
	}
	return repo + "@sha256:" + hexDigest, hexDigest, nil
}

// archiveTag is the name the image is given inside the archive: the tag from
// the pod spec when there is one, otherwise the repository tagged with the
// short digest.
func archiveTag(t *target) string {
	repo, _, _ := strings.Cut(t.Ref, "@")
	spec := t.Container.Image
	if _, _, pinned := strings.Cut(spec, "@"); !pinned {
		if i := strings.LastIndexByte(spec, ':'); i >= 0 && !strings.Contains(spec[i:], "/") {
			return repo + spec[i:]
		}
	}
	return repo + ":" + t.Digest[:12]
}

// defaultOutput is ./<image>_<shortdigest>.tar.
func defaultOutput(t *target) string {
	repo, _, _ := strings.Cut(t.Ref, "@")
	return fmt.Sprintf("%s_%s.tar", path.Base(repo), t.Digest[:12])
}

// collectAuths reads the pull secrets referenced by the pod and its service
// account. Secrets that cannot be read are reported through warn and skipped,
// leaving the pull to proceed anonymously for those registries.
func collectAuths(ctx context.Context, cs kubernetes.Interface, pod *corev1.Pod, warn func(string, ...any)) map[string]fetchproto.Auth {
	names := []string{}
	seen := map[string]bool{}
	add := func(refs []corev1.LocalObjectReference) {
		for _, r := range refs {
			if r.Name != "" && !seen[r.Name] {
				seen[r.Name] = true
				names = append(names, r.Name)
			}
		}
	}
	add(pod.Spec.ImagePullSecrets)
	saName := pod.Spec.ServiceAccountName
	if saName == "" {
		saName = "default"
	}
	if sa, err := cs.CoreV1().ServiceAccounts(pod.Namespace).Get(ctx, saName, metav1.GetOptions{}); err == nil {
		add(sa.ImagePullSecrets)
	} else {
		warn("could not read service account %s (%s); its pull secrets are not used", saName, err)
	}

	auths := map[string]fetchproto.Auth{}
	for _, n := range names {
		secret, err := cs.CoreV1().Secrets(pod.Namespace).Get(ctx, n, metav1.GetOptions{})
		if err != nil {
			warn("could not read pull secret %s (%s); continuing without it", n, err)
			continue
		}
		data, ok := secret.Data[corev1.DockerConfigJsonKey]
		if !ok {
			data, ok = secret.Data[corev1.DockerConfigKey]
		}
		if !ok {
			continue
		}
		entries, err := fetchproto.ParseDockerConfig(data)
		if err != nil {
			warn("pull secret %s: %s", n, err)
			continue
		}
		// Earlier secrets win, as with kubelet: pod secrets precede the
		// service account's.
		for host, a := range entries {
			if _, exists := auths[host]; !exists {
				auths[host] = a
			}
		}
	}
	return auths
}

// carrierName returns a unique ephemeral container name. Names must be unique
// within a pod, and every pull leaves its carrier behind in the pod spec.
func carrierName() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return "kelper-pull-" + hex.EncodeToString(b)
}

// fetcherPath is where the fetcher is written inside the carrier.
const fetcherPath = "/tmp/kelper-fetch"

// carrierContainer builds the ephemeral container that hosts the fetcher.
//
// Its process is a shell that idles for lifetime and then exits, so the
// carrier ends on its own even if kelper dies mid-pull; when PID 1 exits the
// kernel kills the rest of the container, fetcher included. The TERM trap is
// required for early teardown: PID 1 ignores signals it does not handle.
func carrierContainer(t *target, name, image string, lifetimeSeconds int) corev1.EphemeralContainer {
	script := fmt.Sprintf(
		"trap 'rm -f %[1]s; exit 0' TERM; sleep %[2]d & wait; rm -f %[1]s",
		fetcherPath, lifetimeSeconds)

	uid, gid := runAsIDs(t)
	return corev1.EphemeralContainer{
		EphemeralContainerCommon: corev1.EphemeralContainerCommon{
			Name:  name,
			Image: image,
			// The target image is already on the node because the target
			// is running it; IfNotPresent keeps a :latest tag from pulling.
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         []string{"sh", "-c", script},
			// The settings Pod Security "restricted" and common admission
			// policies require, so the carrier is admitted where the pod was.
			SecurityContext: &corev1.SecurityContext{
				RunAsNonRoot:             ptr.To(true),
				RunAsUser:                ptr.To(uid),
				RunAsGroup:               ptr.To(gid),
				AllowPrivilegeEscalation: ptr.To(false),
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
		},
	}
}

// runAsIDs reuses the target's non-root user and group, from the container
// and then the pod, falling back to fallbackUID.
func runAsIDs(t *target) (uid, gid int64) {
	uid, gid = fallbackUID, fallbackUID
	if psc := t.Pod.Spec.SecurityContext; psc != nil {
		if psc.RunAsUser != nil && *psc.RunAsUser != 0 {
			uid = *psc.RunAsUser
		}
		if psc.RunAsGroup != nil {
			gid = *psc.RunAsGroup
		}
	}
	if csc := t.Container.SecurityContext; csc != nil {
		if csc.RunAsUser != nil && *csc.RunAsUser != 0 {
			uid = *csc.RunAsUser
		}
		if csc.RunAsGroup != nil {
			gid = *csc.RunAsGroup
		}
	}
	return uid, gid
}
