package neat_test

import (
	"strings"
	"testing"

	"github.com/jthunderbird/kelper/internal/neat"
)

const deploymentYAML = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: myapp
  namespace: default
  uid: abc-123
  resourceVersion: "12345"
  creationTimestamp: "2024-01-01T00:00:00Z"
  generation: 3
spec:
  progressDeadlineSeconds: 600
  revisionHistoryLimit: 10
  selector:
    matchLabels:
      app: myapp
  template:
    metadata:
      creationTimestamp: null
    spec:
      containers:
      - name: app
        image: myimage:latest
        terminationMessagePath: /dev/termination-log
        terminationMessagePolicy: File
      dnsPolicy: ClusterFirst
      restartPolicy: Always
      schedulerName: default-scheduler
      terminationGracePeriodSeconds: 30
status:
  availableReplicas: 1
`

func TestNeatRemovesUID(t *testing.T) {
	result, err := neat.Clean([]byte(deploymentYAML))
	if err != nil {
		t.Fatalf("Clean returned error: %v", err)
	}
	if strings.Contains(string(result), "uid:") {
		t.Error("neat output should not contain uid")
	}
}

func TestNeatRemovesStatus(t *testing.T) {
	result, err := neat.Clean([]byte(deploymentYAML))
	if err != nil {
		t.Fatalf("Clean returned error: %v", err)
	}
	if strings.Contains(string(result), "status:") {
		t.Error("neat output should not contain status")
	}
}

func TestNeatRemovesResourceVersion(t *testing.T) {
	result, err := neat.Clean([]byte(deploymentYAML))
	if err != nil {
		t.Fatalf("Clean returned error: %v", err)
	}
	if strings.Contains(string(result), "resourceVersion") {
		t.Error("neat output should not contain resourceVersion")
	}
}

func TestNeatPreservesName(t *testing.T) {
	result, err := neat.Clean([]byte(deploymentYAML))
	if err != nil {
		t.Fatalf("Clean returned error: %v", err)
	}
	if !strings.Contains(string(result), "name: myapp") {
		t.Error("neat output should preserve resource name")
	}
}
