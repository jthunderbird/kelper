package neat

import (
	"sigs.k8s.io/yaml"
)

// genericDenylist — fields removed from all resource kinds.
var genericDenylist = [][]string{
	{"metadata", "uid"},
	{"metadata", "resourceVersion"},
	{"metadata", "creationTimestamp"},
	{"metadata", "generation"},
	{"metadata", "ownerReferences"},
	{"metadata", "generateName"},
	{"metadata", "finalizers"},
	{"metadata", "managedFields"},
	{"status"},
}

// kindDenylist — fields removed per resource kind.
var kindDenylist = map[string][][]string{
	"Deployment": {
		{"spec", "progressDeadlineSeconds"},
		{"spec", "revisionHistoryLimit"},
		{"spec", "template", "metadata", "creationTimestamp"},
		{"spec", "template", "spec", "terminationGracePeriodSeconds"},
		{"spec", "template", "spec", "dnsPolicy"},
		{"spec", "template", "spec", "restartPolicy"},
		{"spec", "template", "spec", "schedulerName"},
	},
	"StatefulSet": {
		{"spec", "template", "metadata", "creationTimestamp"},
		{"spec", "template", "spec", "terminationGracePeriodSeconds"},
		{"spec", "template", "spec", "dnsPolicy"},
		{"spec", "template", "spec", "restartPolicy"},
		{"spec", "template", "spec", "schedulerName"},
	},
	"DaemonSet": {
		{"spec", "template", "metadata", "creationTimestamp"},
		{"spec", "template", "spec", "terminationGracePeriodSeconds"},
		{"spec", "template", "spec", "dnsPolicy"},
		{"spec", "template", "spec", "restartPolicy"},
		{"spec", "template", "spec", "schedulerName"},
	},
	"Pod": {
		{"spec", "terminationGracePeriodSeconds"},
		{"spec", "dnsPolicy"},
		{"spec", "restartPolicy"},
		{"spec", "schedulerName"},
		{"spec", "nodeName"},
		{"spec", "serviceAccountName"},
		{"spec", "enableServiceLinks"},
		{"spec", "preemptionPolicy"},
		{"spec", "priority"},
	},
	"Service": {
		{"spec", "clusterIP"},
		{"spec", "clusterIPs"},
		{"spec", "internalTrafficPolicy"},
		{"spec", "ipFamilies"},
		{"spec", "ipFamilyPolicy"},
		{"spec", "sessionAffinity"},
	},
}

// Clean strips server-populated default fields from Kubernetes YAML.
// Handles both single resources and kind: List.
func Clean(yamlBytes []byte) ([]byte, error) {
	var obj map[string]interface{}
	if err := yaml.Unmarshal(yamlBytes, &obj); err != nil {
		return nil, err
	}

	kind, _ := obj["kind"].(string)
	if kind == "List" {
		return cleanList(obj)
	}
	cleanObject(obj, kind)
	return yaml.Marshal(obj)
}

func cleanList(obj map[string]interface{}) ([]byte, error) {
	items, _ := obj["items"].([]interface{})
	for i, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		kind, _ := m["kind"].(string)
		cleanObject(m, kind)
		items[i] = m
	}
	obj["items"] = items
	return yaml.Marshal(obj)
}

func cleanObject(obj map[string]interface{}, kind string) {
	for _, path := range genericDenylist {
		deletePath(obj, path)
	}
	for _, path := range kindDenylist[kind] {
		deletePath(obj, path)
	}
	// Strip terminationMessagePath/Policy from all container slices.
	stripContainerFields(obj, "spec", "containers")
	stripContainerFields(obj, "spec", "initContainers")
	stripContainerFields(obj, "spec", "template", "spec", "containers")
	stripContainerFields(obj, "spec", "template", "spec", "initContainers")
}

func deletePath(obj map[string]interface{}, path []string) {
	if len(path) == 1 {
		delete(obj, path[0])
		return
	}
	next, ok := obj[path[0]].(map[string]interface{})
	if !ok {
		return
	}
	deletePath(next, path[1:])
}

func stripContainerFields(obj map[string]interface{}, path ...string) {
	cur := obj
	for _, key := range path[:len(path)-1] {
		next, ok := cur[key].(map[string]interface{})
		if !ok {
			return
		}
		cur = next
	}
	containers, ok := cur[path[len(path)-1]].([]interface{})
	if !ok {
		return
	}
	for _, c := range containers {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		delete(m, "terminationMessagePath")
		delete(m, "terminationMessagePolicy")
	}
}
