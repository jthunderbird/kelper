package healthcheck

import (
	"context"
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/jthunderbird/kelper/internal/output"
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/spf13/cobra"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var sectionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))

// Command returns the cobra command for healthcheck / health.
func Command(cs **kubernetes.Clientset) *cobra.Command {
	var namespace string
	var allNamespaces bool

	cmd := &cobra.Command{
		Use:     "healthcheck",
		Aliases: []string{"health"},
		Short:   "Check cluster health",
		RunE: func(cmd *cobra.Command, args []string) error {
			// No targeting flags → TUI mode (placeholder until Task 8).
			if !allNamespaces && namespace == "" {
				return runTUI(*cs)
			}
			ns := namespace
			if allNamespaces {
				ns = ""
			}
			return RunTable(*cs, ns)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "namespace to check")
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "check all namespaces")
	return cmd
}

// RunTable runs a non-interactive healthcheck and prints tables to stdout.
// Returns a non-nil error if any unhealthy resources are found (triggers exit code 1).
func RunTable(cs *kubernetes.Clientset, namespace string) error {
	ctx := context.Background()

	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		output.Errorf(os.Stderr, "list pods: %s", err)
		os.Exit(1)
	}

	var unhealthyPods []corev1.Pod
	for _, p := range pods.Items {
		if IsUnhealthyPod(p) {
			unhealthyPods = append(unhealthyPods, p)
		}
	}

	unhealthyWorkloads, err := listUnhealthyWorkloads(ctx, cs, namespace)
	if err != nil {
		output.Errorf(os.Stderr, "list workloads: %s", err)
		os.Exit(1)
	}

	noBorders := tablewriter.WithBorders(tw.Border{
		Left:   tw.Off,
		Right:  tw.Off,
		Top:    tw.Off,
		Bottom: tw.Off,
	})

	// Print unhealthy pods table.
	fmt.Println(sectionStyle.Render("Unhealthy Pods"))
	if len(unhealthyPods) == 0 {
		fmt.Println("  (none)")
	} else {
		t := tablewriter.NewTable(os.Stdout, noBorders)
		t.Header([]string{"NAMESPACE", "NAME", "READY", "STATUS"})
		for _, p := range unhealthyPods {
			t.Append([]string{p.Namespace, p.Name, podReadyString(p), podStatusString(p)})
		}
		t.Render()
	}
	fmt.Println()

	// Print unhealthy workloads table.
	fmt.Println(sectionStyle.Render("Unhealthy Workloads"))
	if len(unhealthyWorkloads) == 0 {
		fmt.Println("  (none)")
	} else {
		t := tablewriter.NewTable(os.Stdout, noBorders)
		t.Header([]string{"NAMESPACE", "KIND", "NAME", "AVAILABLE", "DESIRED"})
		for _, w := range unhealthyWorkloads {
			t.Append([]string{w.namespace, w.kind, w.name,
				fmt.Sprint(w.available), fmt.Sprint(w.desired)})
		}
		t.Render()
	}
	fmt.Println()

	fmt.Printf("Summary: %d unhealthy pod(s), %d unhealthy workload(s)\n",
		len(unhealthyPods), len(unhealthyWorkloads))

	if len(unhealthyPods) > 0 || len(unhealthyWorkloads) > 0 {
		return fmt.Errorf("unhealthy resources found")
	}
	return nil
}

// IsUnhealthyPod returns true if a pod is not in a healthy terminal or running state.
func IsUnhealthyPod(p corev1.Pod) bool {
	if p.Status.Phase == corev1.PodSucceeded {
		return false
	}
	for _, cs := range p.Status.ContainerStatuses {
		if !cs.Ready {
			return true
		}
	}
	return false
}

type workloadStatus struct {
	namespace string
	kind      string
	name      string
	available int32
	desired   int32
}

func listUnhealthyWorkloads(ctx context.Context, cs *kubernetes.Clientset, namespace string) ([]workloadStatus, error) {
	var results []workloadStatus

	// Deployments.
	deploys, err := cs.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, d := range deploys.Items {
		if d.Spec.Replicas == nil {
			continue
		}
		desired := *d.Spec.Replicas
		available := d.Status.AvailableReplicas
		if available < desired {
			results = append(results, workloadStatus{d.Namespace, "Deployment", d.Name, available, desired})
		}
	}

	// StatefulSets.
	stss, err := cs.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, s := range stss.Items {
		if s.Spec.Replicas == nil {
			continue
		}
		desired := *s.Spec.Replicas
		ready := s.Status.ReadyReplicas
		if ready < desired {
			results = append(results, workloadStatus{s.Namespace, "StatefulSet", s.Name, ready, desired})
		}
	}

	// DaemonSets.
	dss, err := cs.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, d := range dss.Items {
		desired := d.Status.DesiredNumberScheduled
		ready := d.Status.NumberReady
		if ready < desired {
			results = append(results, workloadStatus{d.Namespace, "DaemonSet", d.Name, ready, desired})
		}
	}

	// Jobs — flag Failed conditions.
	jobs, err := cs.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, j := range jobs.Items {
		for _, c := range j.Status.Conditions {
			if c.Type == batchv1.JobFailed && c.Status == "True" {
				results = append(results, workloadStatus{j.Namespace, "Job", j.Name, 0, 1})
				break
			}
		}
	}

	// CronJobs — list only (individual job failures caught above).
	_, err = cs.BatchV1().CronJobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	return results, nil
}

func podReadyString(p corev1.Pod) string {
	total := len(p.Status.ContainerStatuses)
	ready := 0
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
	}
	return fmt.Sprintf("%d/%d", ready, total)
}

func podStatusString(p corev1.Pod) string {
	for _, cs := range p.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			return cs.State.Waiting.Reason
		}
	}
	return string(p.Status.Phase)
}

// runTUI is called when no targeting flags are given.
// Full TUI implemented in tui.go (Task 8). For now falls back to default namespace.
func runTUI(cs *kubernetes.Clientset) error {
	fmt.Fprintln(os.Stderr, "Interactive TUI not yet implemented. Running against default namespace.")
	fmt.Fprintln(os.Stderr, "To target a specific namespace: kelper healthcheck -n <namespace>")
	return RunTable(cs, "default")
}
