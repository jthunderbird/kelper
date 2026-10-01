package pull

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// execer runs commands in a pod's containers over the pods/exec subresource.
type execer struct {
	cs        kubernetes.Interface
	config    *rest.Config
	namespace string
	pod       string
}

// run executes command in container, wiring the given streams. A nil stdin
// sends no input. A non-zero exit is returned as an error. When the caller
// takes no stderr it is captured and appended to any error, since the API
// rejects an exec with no streams at all.
func (e *execer) run(ctx context.Context, container string, command []string, stdin io.Reader, stdout, stderr io.Writer) error {
	var captured *bytes.Buffer
	if stderr == nil {
		captured = &bytes.Buffer{}
		stderr = captured
	}
	err := e.stream(ctx, container, command, stdin, stdout, stderr)
	if err != nil && captured != nil {
		if msg := strings.TrimSpace(captured.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
	}
	return err
}

func (e *execer) stream(ctx context.Context, container string, command []string, stdin io.Reader, stdout, stderr io.Writer) error {
	req := e.cs.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(e.namespace).Name(e.pod).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdin:     stdin != nil,
			Stdout:    stdout != nil,
			Stderr:    stderr != nil,
		}, scheme.ParameterCodec)

	// Prefer WebSockets and fall back to SPDY for older API servers, the same
	// negotiation kubectl exec performs.
	ws, err := remotecommand.NewWebSocketExecutor(e.config, "GET", req.URL().String())
	if err != nil {
		return err
	}
	spdy, err := remotecommand.NewSPDYExecutor(e.config, "POST", req.URL())
	if err != nil {
		return err
	}
	exec, err := remotecommand.NewFallbackExecutor(ws, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return err
	}
	return exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Stderr: stderr})
}
