package resources

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jthunderbird/kelper/internal/output"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// Command returns the cobra command for resources / resource / res.
func Command(cs **kubernetes.Clientset) *cobra.Command {
	var namespace string
	var allNamespaces bool

	cmd := &cobra.Command{
		Use:     "resources",
		Aliases: []string{"resource", "res"},
		Short:   "Show resource limits and requests per pod",
		RunE: func(cmd *cobra.Command, args []string) error {
			ns := namespace
			if allNamespaces {
				ns = ""
			}
			var podName string
			if len(args) > 0 {
				podName = args[0]
			}
			return run(*cs, ns, podName)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "all namespaces")
	return cmd
}

func run(cs *kubernetes.Clientset, namespace, podName string) error {
	ctx := context.Background()
	if podName != "" {
		pod, err := cs.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			output.Errorf(os.Stderr, "get pod: %s", err)
			os.Exit(1)
		}
		RenderPod(os.Stdout, *pod)
		return nil
	}
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		output.Errorf(os.Stderr, "list pods: %s", err)
		os.Exit(1)
	}
	for _, pod := range pods.Items {
		RenderPod(os.Stdout, pod)
	}
	return nil
}

// RenderPod writes resource limits/requests for a single pod to w.
func RenderPod(w io.Writer, pod corev1.Pod) {
	output.PodHeader(w, pod.Name, pod.Namespace)

	fmt.Fprintln(w, "  initContainers:")
	if len(pod.Spec.InitContainers) == 0 {
		fmt.Fprintln(w, "    (none)")
	} else {
		for _, c := range pod.Spec.InitContainers {
			fmt.Fprintf(w, "    %s:\n", c.Name)
			renderResources(w, c.Resources)
		}
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "  containers:")
	if len(pod.Spec.Containers) == 0 {
		fmt.Fprintln(w, "    (none)")
	} else {
		for _, c := range pod.Spec.Containers {
			fmt.Fprintf(w, "    %s:\n", c.Name)
			renderResources(w, c.Resources)
		}
	}
	fmt.Fprintln(w)
}

func renderResources(w io.Writer, r corev1.ResourceRequirements) {
	if len(r.Limits) == 0 && len(r.Requests) == 0 {
		fmt.Fprintln(w, "      (none)")
		return
	}
	data, _ := yaml.Marshal(r)
	for _, line := range splitLines(string(data)) {
		if line != "" {
			fmt.Fprintf(w, "      %s\n", line)
		}
	}
}

func splitLines(s string) []string {
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}
