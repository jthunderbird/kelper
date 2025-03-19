package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"flag"
	"fmt"
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
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Metadata   Metadata `yaml:"metadata"`
	Data       map[string]string `yaml:"data"`
}

type GenericResource struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Metadata   Metadata `yaml:"metadata"`
}

func main() {
	kubeconfig := flag.String("kubeconfig", "", "Path to the kubeconfig file")
	namespace := flag.String("namespace", "default", "Namespace to list pods from")
	listPods := flag.Bool("list-pods", false, "List all pods in the namespace along with their init containers and containers")
	flag.Parse()

	if *listPods {
		listAllPods(*kubeconfig, *namespace)
		return
	}

	if len(flag.Args()) < 1 {
		fmt.Println("Usage: kustomkubectl [kubectl arguments]")
		return
	}

	cmd := exec.Command("kubectl", flag.Args()...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		fmt.Printf("Error running kubectl: %v\n", err)
		return
	}

	output := out.String()
	if strings.Contains(output, "kind: Secret") && strings.Contains(strings.Join(flag.Args(), " "), "-o yaml") {
		decodeAndPrintSecrets(output)
	} else if strings.Contains(strings.Join(flag.Args(), " "), "-o yaml") {
		removeMetadataFields(output)
	} else {
		fmt.Print(output)
	}
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

	// clean up yaml output
	if metadata, ok := resource["metadata"].(map[string]interface{}); ok {
		delete(metadata, "creationTimestamp")
		delete(metadata, "uid")
	}
	delete(resource, "metadata.uid")
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
