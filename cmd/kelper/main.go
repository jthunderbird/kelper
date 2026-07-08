package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type Metadata struct {
	Name       string `yaml:"name"`
	Namespace  string `yaml:"namespace"`
	UID        string `yaml:"uid,omitempty"`
	Generation int64  `yaml:"generation,omitempty"`
}

type Secret struct {
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Metadata   Metadata          `yaml:"metadata"`
	Data       map[string]string `yaml:"data"`
}

type GenericResource struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Metadata   Metadata `yaml:"metadata"`
}

func main() {
	global, rest := parseGlobal(os.Args[1:])

	// No command at all -> show the top-level help.
	if len(rest) == 0 {
		printRootHelp(os.Stdout)
		return
	}

	switch rest[0] {
	case "-h", "--help":
		printRootHelp(os.Stdout)
		return
	case "-v", "--version":
		fmt.Printf("kelper %s\n", appVersion)
		return
	}

	// Dispatch to a native command when the first token names one.
	if cmd, ok := lookupCommand(rest[0]); ok {
		cmd.run(global, rest[1:])
		return
	}

	// Otherwise forward the whole invocation to kubectl.
	runKubectlPassthrough(global, rest)
}

// parseGlobal pulls any leading kelper global flags off the argument list and
// returns the remainder for command dispatch. A leading token that is not a
// kelper global flag (e.g. a kubectl flag, or -h/--help) is handed back
// untouched so it can be dispatched or passed through.
func parseGlobal(args []string) (*globalOpts, []string) {
	g := &globalOpts{}
	fs := flag.NewFlagSet("kelper", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&g.kubeconfig, "kubeconfig", "", "Path to the kubeconfig file")
	if err := fs.Parse(args); err != nil {
		return g, args
	}
	return g, fs.Args()
}

// runKubectlPassthrough runs kubectl with the given args against the resolved
// (failover-aware) kubeconfig. It streams by default so interactive commands
// keep kubectl's stdio behavior, and only buffers output for kelper's explicit
// YAML transforms.
func runKubectlPassthrough(global *globalOpts, args []string) {
	resolved, cleanup, err := resolveKubeconfig(global.kubeconfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer cleanup()

	cmd := exec.Command("kubectl", args...)
	if resolved != "" {
		// Hand kubectl the resolved single-server kubeconfig.
		cmd.Env = append(os.Environ(), "KUBECONFIG="+resolved)
	}
	if !shouldTransformKubectlOutput(args) {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Error running kubectl: %v\n", err)
			os.Exit(1)
		}
		return
	}

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running kubectl: %v\n", err)
		os.Exit(1)
	}

	output := out.String()
	switch {
	case strings.Contains(output, "kind: Secret"):
		decodeAndPrintSecrets(output)
	default:
		removeMetadataFields(output)
	}
}

func shouldTransformKubectlOutput(args []string) bool {
	if kubectlVerb(args) != "get" {
		return false
	}
	if hasWatchFlag(args) {
		return false
	}
	return kubectlOutputFormat(args) == "yaml"
}

func kubectlVerb(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return ""
		}
		if arg == "" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			if consumesNextArg(arg) && i+1 < len(args) {
				i++
			}
			continue
		}
		return arg
	}
	return ""
}

func hasWatchFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "-w" || arg == "--watch" || strings.HasPrefix(arg, "--watch=") {
			return true
		}
	}
	return false
}

func kubectlOutputFormat(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return ""
		}
		switch {
		case arg == "-o" || arg == "--output":
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		case strings.HasPrefix(arg, "-o="):
			return strings.TrimPrefix(arg, "-o=")
		case strings.HasPrefix(arg, "--output="):
			return strings.TrimPrefix(arg, "--output=")
		case strings.HasPrefix(arg, "-o") && len(arg) > len("-o"):
			return strings.TrimPrefix(arg, "-o")
		}
	}
	return ""
}

func consumesNextArg(arg string) bool {
	if strings.Contains(arg, "=") {
		return false
	}

	switch arg {
	case "-n", "--namespace",
		"--context",
		"--kubeconfig",
		"--cluster",
		"--user",
		"--as",
		"--as-group",
		"--as-uid",
		"--cache-dir",
		"--certificate-authority",
		"--client-certificate",
		"--client-key",
		"--profile",
		"--request-timeout",
		"--server",
		"--tls-server-name",
		"--token",
		"--username",
		"--password",
		"-o",
		"--output":
		return true
	}
	return false
}

func decodeAndPrintSecrets(output string) {
	var secret Secret
	err := yaml.Unmarshal([]byte(output), &secret)
	if err != nil {
		fmt.Printf("Error unmarshaling yaml: %v\n", err)
		return
	}

	for k, v := range secret.Data {
		decodedValue, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			fmt.Printf("Error decoding base64 value for key %s: %v\n", k, err)
			continue
		}
		secret.Data[k] = string(decodedValue)
	}

	decodedYAML, err := yaml.Marshal(&secret)
	if err != nil {
		fmt.Printf("Error marshaling yaml: %v\n", err)
		return
	}

	fmt.Print(string(decodedYAML))
}

func removeMetadataFields(output string) {
	var resource map[string]interface{}
	err := yaml.Unmarshal([]byte(output), &resource)
	if err != nil {
		fmt.Printf("Error unmarshaling yaml: %v\n", err)
		return
	}

	// clean up yaml output. yaml.v2 decodes nested mappings as
	// map[interface{}]interface{}, not map[string]interface{}.
	if metadata, ok := resource["metadata"].(map[interface{}]interface{}); ok {
		delete(metadata, "creationTimestamp")
		delete(metadata, "uid")
	}
	// delete(resource, "metadata.generation")
	// delete(resource, "metadata.ownerReferences")
	// delete(resource, "metadata.generateName")
	// delete(resource, "metadata.finalizers")
	// delete(resource, "spec.progressDeadlineSeconds")
	// delete(resource, "spec.revisionHistoryLimit")
	// delete(resource, "spec.template.metadata.creationTimestamp")
	// delete(resource, "spec.template.spec.containers.[].livenessProbe")
	// delete(resource, "spec.template.spec.containers.[].readinessProbe")
	// delete(resource, "spec.template.spec.containers.[].terminationMessagePath")
	// delete(resource, "spec.template.spec.containers.[].terminationMessagePolicy")
	// delete(resource, "spec.template.spec.dnsPolicy")
	// delete(resource, "spec.template.spec.restartPolicy")
	// delete(resource, "spec.template.spec.schedulerName")
	// delete(resource, "spec.template.spec.terminationGracePerionSeconds")
	// delete(resource, "spec.clusterIP")
	// delete(resource, "spec.clusterIPs")
	// delete(resource, "spec.internalTrafficPolicy")
	// delete(resource, "spec.ipFamilies")
	// delete(resource, "spec.ipFamilyPolicy")
	// delete(resource, "spec.sessionAffinity")
	// delete(resource, "metadata.resourceVersion")
	delete(resource, "status")

	updatedYAML, err := yaml.Marshal(&resource)
	if err != nil {
		fmt.Printf("Error marshaling yaml: %v\n", err)
		return
	}

	fmt.Print(string(updatedYAML))
}

func listAllPods(kubeconfig, namespace string) {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		fmt.Printf("Error building kubeconfig: %v\n", err)
		return
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Printf("Error creating Kubernetes client: %v\n", err)
		return
	}

	pods, err := clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Error listing pods: %v\n", err)
		return
	}

	for _, pod := range pods.Items {
		fmt.Printf("Pod: %s, Namespace: %s\n", pod.Name, pod.Namespace)
		fmt.Println("Init Containers:")
		for _, initContainer := range pod.Spec.InitContainers {
			fmt.Printf("  Name: %s, Image: %s\n", initContainer.Name, initContainer.Image)
		}

		fmt.Println("Containers:")
		for _, container := range pod.Spec.Containers {
			fmt.Printf("  Name: %s, Image: %s\n", container.Name, container.Image)
		}
	}
}
