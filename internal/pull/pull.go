// Package pull implements `kelper pull`, which saves the image a pod is
// running to a local archive using only Kubernetes API access.
//
// The API offers no way to read image content, so kelper adds an ephemeral
// "carrier" container to the target pod, injects a small static fetcher into
// it over exec, and has the fetcher pull the image from the registry through
// the pod's own network path while streaming the archive back over exec.
package pull

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jthunderbird/kelper/internal/client"
	"github.com/jthunderbird/kelper/internal/pull/fetchproto"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	utilexec "k8s.io/client-go/util/exec"
)

// Options are the flags of `kelper pull`.
type Options struct {
	Namespace   string
	Container   string
	Output      string
	Format      string
	Platform    string
	HelperImage string
	CAFile      string
	Insecure    bool
	Timeout     time.Duration
	Yes         bool
}

const longHelp = `Save the image a pod's container is running to a local archive, using only
Kubernetes API access. Useful when the image comes from a registry the cluster
can reach but your workstation cannot.

kelper adds an ephemeral container to the pod, injects a small fetcher into it,
and pulls the image by its running digest through the pod's own network path,
streaming the archive back to your machine. Pull secrets from the pod and its
service account are used for authentication.

This changes the pod in the same way kubectl debug does: the stopped helper
stays listed on the pod until the pod is next recreated, though nothing keeps
running. kelper asks before making the change; pass -y to skip the prompt.

The helper runs the target's own image, which must provide sh, cat, chmod,
sleep and rm. For images without a shell (distroless, scratch) pass
--helper-image with any image that has them and is available to the node.

Limitations:
  - Images side-loaded onto nodes (ctr import, air-gap tarballs) or referenced
    by image ID cannot be pulled: no registry serves them.
  - Registry mirrors and credentials configured on the node (containerd mirror
    config, kubelet credential providers) are not visible from inside the pod.
    The pull goes to the registry named in the image reference, using the pod's
    pull secrets only.

Required RBAC: get pods, nodes, serviceaccounts and secrets; patch
pods/ephemeralcontainers; create pods/exec.`

// Command returns the pull command. kubeconfigPath points at the root
// --kubeconfig flag's value, which is read once the command runs.
func Command(kubeconfigPath *string) *cobra.Command {
	opts := Options{}
	cmd := &cobra.Command{
		Use:   "pull POD [-c CONTAINER]",
		Short: "Save the image a pod is running to a local archive",
		Long:  longHelp,
		Example: `  kelper pull keycloak-869969bcc7-sk7bf -n keycloak
  kelper pull web-0 -c app -o web.tar
  kelper pull web-0 --format oci --helper-image busybox:1.37`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return Run(ctx, *kubeconfigPath, args[0], opts, cmd.InOrStdin(), cmd.ErrOrStderr())
		},
	}
	f := cmd.Flags()
	f.StringVarP(&opts.Namespace, "namespace", "n", "", "namespace of the pod (default: the current context's namespace)")
	f.StringVarP(&opts.Container, "container", "c", "", "container whose image to pull (default: the pod's default container)")
	f.StringVarP(&opts.Output, "output", "o", "", "archive path (default: ./<image>_<shortdigest>.tar)")
	f.StringVar(&opts.Format, "format", string(fetchproto.FormatDocker), "archive format: docker (docker load) or oci (OCI image layout)")
	f.StringVar(&opts.Platform, "platform", "", "platform to select from a multi-arch image, e.g. linux/arm64 (default: the node's)")
	f.StringVar(&opts.HelperImage, "helper-image", "", "image for the helper container, for targets without a shell")
	f.StringVar(&opts.CAFile, "ca", "", "PEM file of extra certificate authorities to trust for the registry")
	f.BoolVar(&opts.Insecure, "insecure-skip-tls-verify", false, "skip registry TLS verification and allow plain HTTP")
	f.DurationVar(&opts.Timeout, "timeout", 10*time.Minute, "maximum time for the pull; the helper container stops by then at the latest")
	f.BoolVarP(&opts.Yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}

// Run executes a pull. Status and the prompt go to stderr; the prompt answer is
// read from stdin.
func Run(ctx context.Context, kubeconfigPath, podName string, opts Options, stdin io.Reader, stderr io.Writer) error {
	format := fetchproto.Format(opts.Format)
	if format != fetchproto.FormatDocker && format != fetchproto.FormatOCI {
		return fmt.Errorf("unknown --format %q (want docker or oci)", opts.Format)
	}
	if opts.Timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}
	var ca string
	if opts.CAFile != "" {
		b, err := os.ReadFile(opts.CAFile)
		if err != nil {
			return fmt.Errorf("read --ca: %w", err)
		}
		ca = string(b)
	}

	config, err := client.RestConfig(kubeconfigPath)
	if err != nil {
		return fmt.Errorf("could not connect to cluster: %w", err)
	}
	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	namespace := opts.Namespace
	if namespace == "" {
		if namespace, err = client.Namespace(kubeconfigPath); err != nil {
			return err
		}
	}

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	warn := func(format string, args ...any) { fmt.Fprintf(stderr, "warning: "+format+"\n", args...) }

	t, err := resolveTarget(ctx, cs, namespace, podName, opts.Container)
	if err != nil {
		return err
	}
	nodeArch, platform, err := resolvePlatform(ctx, cs, t.Pod.Spec.NodeName, opts.Platform)
	if err != nil {
		return err
	}
	fetcher, err := fetcherBinary(nodeArch)
	if err != nil {
		return err
	}
	auths := collectAuths(ctx, cs, t.Pod, warn)

	ex := &execer{cs: cs, config: config, namespace: namespace, pod: t.Pod.Name}
	image := opts.HelperImage
	if image == "" {
		image = t.Container.Image
		if err := probeTarget(ctx, ex, t); err != nil {
			return err
		}
	}

	output := opts.Output
	if output == "" {
		output = defaultOutput(t)
	}
	name := carrierName()
	if !opts.Yes {
		ok, err := confirm(stdin, stderr, t, name, image, opts.Timeout)
		if err != nil || !ok {
			return err
		}
	}

	p := &puller{cs: cs, ex: ex, t: t, name: name, stderr: stderr}
	return p.pull(ctx, image, fetcher, fetchproto.Request{
		Ref:      t.Ref,
		Tag:      archiveTag(t),
		Platform: platform,
		Format:   format,
		Auths:    auths,
		CA:       ca,
		Insecure: opts.Insecure,
	}, output, opts.Timeout)
}

// resolvePlatform reads the node's platform, which decides both the fetcher
// binary and, unless overridden, the image platform to select.
func resolvePlatform(ctx context.Context, cs kubernetes.Interface, nodeName, override string) (arch, platform string, err error) {
	node, err := cs.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		if override == "" {
			return "", "", fmt.Errorf("could not read node %s to detect its platform (%w); pass --platform", nodeName, err)
		}
		// Without the node, assume it matches the requested platform.
		parts := strings.Split(override, "/")
		if len(parts) < 2 {
			return "", "", fmt.Errorf("invalid --platform %q (want os/arch)", override)
		}
		return parts[1], override, nil
	}
	info := node.Status.NodeInfo
	platform = override
	if platform == "" {
		platform = info.OperatingSystem + "/" + info.Architecture
	}
	return info.Architecture, platform, nil
}

// bootstrapTools are the programs the carrier needs before the fetcher can
// take over: a shell to receive and start it, and the commands the carrier's
// own lifecycle script uses.
var bootstrapTools = []string{"cat", "chmod", "sleep", "rm"}

// probeTarget checks the running target container for the bootstrap tools, so
// that a carrier that would fail is never added to the pod. A container that
// is not running cannot be probed; the carrier is attempted anyway.
func probeTarget(ctx context.Context, ex *execer, t *target) error {
	if !t.Running {
		return nil
	}
	script := "for c in " + strings.Join(bootstrapTools, " ") + "; do command -v $c >/dev/null || exit 1; done"
	if err := ex.run(ctx, t.Container.Name, []string{"sh", "-c", script}, nil, nil, nil); err != nil {
		if !toolsMissing(err) {
			return fmt.Errorf("probe container %q: %w", t.Container.Name, err)
		}
		return fmt.Errorf("the image of container %q has no shell or lacks one of %s, so it cannot host the fetcher.\n"+
			"Pass --helper-image with an image that has them and is available to the node, e.g. --helper-image busybox:1.37",
			t.Container.Name, strings.Join(bootstrapTools, ", "))
	}
	return nil
}

// toolsMissing reports whether a probe failed because the image lacks the
// tools: the script exited non-zero, or the runtime could not find sh at all.
// Anything else (RBAC, a stopped container, network) is a real error.
func toolsMissing(err error) bool {
	var exitErr utilexec.ExitError
	if errors.As(err, &exitErr) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "executable file not found") || strings.Contains(msg, "no such file or directory")
}

// confirm explains the change to the pod and asks to proceed.
func confirm(stdin io.Reader, stderr io.Writer, t *target, name, image string, timeout time.Duration) (bool, error) {
	fmt.Fprintf(stderr, "kelper pull will change pod %s/%s:\n", t.Pod.Namespace, t.Pod.Name)
	fmt.Fprintf(stderr, "  - add ephemeral container %q running %s to pull the image from inside the pod\n", name, image)
	fmt.Fprintf(stderr, "  - the container stops when the pull ends, or after %s at the latest\n", timeout)
	fmt.Fprintf(stderr, "  - as with kubectl debug, the stopped container stays listed on the pod\n")
	fmt.Fprintf(stderr, "    until the pod is next recreated; nothing keeps running\n")
	fmt.Fprint(stderr, "Continue? [y/N]: ")
	answer, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if !strings.EqualFold(strings.TrimSpace(answer), "y") {
		fmt.Fprintln(stderr, "Cancelled.")
		return false, nil
	}
	return true, nil
}

// puller runs one pull through a carrier container.
type puller struct {
	cs     kubernetes.Interface
	ex     *execer
	t      *target
	name   string
	stderr io.Writer
}

// carrierGrace keeps the carrier alive a little past kelper's own timeout, so
// that kelper, not the carrier, is what ends a slow pull.
const carrierGrace = 30 * time.Second

func (p *puller) pull(ctx context.Context, image string, fetcher []byte, req fetchproto.Request, output string, timeout time.Duration) error {
	lifetime := int((timeout + carrierGrace).Seconds())
	if err := p.addCarrier(ctx, image, lifetime); err != nil {
		return err
	}
	fmt.Fprintf(p.stderr, "Waiting for helper container %s...\n", p.name)
	if err := p.waitRunning(ctx); err != nil {
		return err
	}
	// From here teardown runs on every path, including Ctrl-C, with its own
	// deadline since ctx may already be done. If it fails, the carrier still
	// exits at the end of its lifetime.
	defer p.stopCarrier()

	inject := fmt.Sprintf("cat > %[1]s && chmod +x %[1]s", fetcherPath)
	var injectErr bytes.Buffer
	if err := p.ex.run(ctx, p.name, []string{"sh", "-c", inject}, bytes.NewReader(fetcher), nil, &injectErr); err != nil {
		return fmt.Errorf("inject fetcher: %w %s", err, strings.TrimSpace(injectErr.String()))
	}

	return p.fetch(ctx, req, output)
}

// addCarrier patches the ephemeral container into the pod.
func (p *puller) addCarrier(ctx context.Context, image string, lifetimeSeconds int) error {
	ec := carrierContainer(p.t, p.name, image, lifetimeSeconds)
	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{"ephemeralContainers": []corev1.EphemeralContainer{ec}},
	})
	if err != nil {
		return err
	}
	_, err = p.cs.CoreV1().Pods(p.t.Pod.Namespace).Patch(ctx, p.t.Pod.Name,
		types.StrategicMergePatchType, patch, metav1.PatchOptions{}, "ephemeralcontainers")
	if err != nil {
		return fmt.Errorf("add helper container: %w", err)
	}
	return nil
}

// startFailures are waiting reasons after which the carrier will not start
// without outside intervention.
var startFailures = map[string]bool{
	"ErrImagePull":               true,
	"ImagePullBackOff":           true,
	"InvalidImageName":           true,
	"ErrImageNeverPull":          true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
	"RunContainerError":          true,
	"StartError":                 true,
}

// waitRunning polls until the carrier runs, fails to start, or ctx ends.
func (p *puller) waitRunning(ctx context.Context) error {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		pod, err := p.cs.CoreV1().Pods(p.t.Pod.Namespace).Get(ctx, p.t.Pod.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if err := carrierState(pod, p.name); err != errNotYet {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("helper container %s did not start in time (%w); if it starts later it stops on its own at its timeout", p.name, ctx.Err())
		case <-tick.C:
		}
	}
}

var errNotYet = errors.New("not started yet")

// carrierState classifies the carrier's status: nil when running, errNotYet
// while starting, or an error explaining why it cannot start.
func carrierState(pod *corev1.Pod, name string) error {
	for _, s := range pod.Status.EphemeralContainerStatuses {
		if s.Name != name {
			continue
		}
		switch {
		case s.State.Running != nil:
			return nil
		case s.State.Terminated != nil:
			return fmt.Errorf("helper container exited before the pull: %s %s",
				s.State.Terminated.Reason, s.State.Terminated.Message)
		case s.State.Waiting != nil && startFailures[s.State.Waiting.Reason]:
			return fmt.Errorf("helper container cannot start: %s: %s\n"+
				"Kubernetes cannot remove ephemeral containers, so it stays in the pod spec "+
				"(and kubelet keeps retrying) until the pod is recreated.\n"+
				"If the image cannot be used, pass --helper-image with one available to the node",
				s.State.Waiting.Reason, s.State.Waiting.Message)
		}
	}
	return errNotYet
}

// fetch runs the fetcher and streams its archive into output, through a temp
// file so a failed pull never leaves a partial archive.
func (p *puller) fetch(ctx context.Context, req fetchproto.Request, output string) error {
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(output), ".kelper-pull-*.tar")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename

	counter := &countingWriter{w: tmp}
	stopProgress := p.progress(counter)
	start := time.Now()

	var fetchErr bytes.Buffer
	err = p.ex.run(ctx, p.name, []string{fetcherPath}, bytes.NewReader(reqJSON), counter, &fetchErr)
	stopProgress()
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if msg := strings.TrimSpace(fetchErr.String()); msg != "" {
			return errors.New(msg)
		}
		if ctx.Err() != nil {
			return fmt.Errorf("pull interrupted: %w", ctx.Err())
		}
		return fmt.Errorf("fetch: %w", err)
	}
	// CreateTemp makes the file owner-only; give the archive normal
	// permissions, as any other downloaded file.
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), output); err != nil {
		return err
	}
	fmt.Fprintf(p.stderr, "Saved %s (%s, %s) in %s\n", output, req.Tag, humanBytes(counter.n.Load()),
		time.Since(start).Round(100*time.Millisecond))
	return nil
}

// progress prints a running byte count while the archive streams in, when
// stderr is a terminal. The returned func stops it.
func (p *puller) progress(c *countingWriter) func() {
	f, ok := p.stderr.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return func() {}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				fmt.Fprint(f, "\r\033[K")
				return
			case <-tick.C:
				fmt.Fprintf(f, "\rPulling %s... %s", p.t.Ref, humanBytes(c.n.Load()))
			}
		}
	}()
	return func() { close(done); <-finished }
}

// stopCarrier signals the carrier's shell to exit. kill is a shell builtin, so
// it must run under sh.
func (p *puller) stopCarrier() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.ex.run(ctx, p.name, []string{"sh", "-c", "kill 1"}, nil, nil, nil); err != nil {
		fmt.Fprintf(p.stderr, "warning: could not stop helper container %s (%s); it stops on its own at its timeout\n", p.name, err)
	}
}

type countingWriter struct {
	w io.Writer
	n atomic.Int64
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n.Add(int64(n))
	return n, err
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
