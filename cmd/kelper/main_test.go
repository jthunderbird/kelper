package main

import (
	"slices"
	"testing"
)

func TestFirstCommandSkipsGlobalFlags(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantCmd   string
		wantIndex int
	}{
		{"bare subcommand", []string{"get", "pods"}, "get", 0},
		{"kubeconfig flag with separate value", []string{"--kubeconfig", "/p", "get", "pods"}, "get", 2},
		{"kubeconfig flag with inline value", []string{"--kubeconfig=/p", "get", "pods"}, "get", 1},
		{"kelper subcommand behind a flag", []string{"--kubeconfig", "/p", "kubeconfig", "readonly"}, "kubeconfig", 2},
		{"flags only", []string{"--kubeconfig", "/p"}, "", 2},
		{"kubectl namespace flag", []string{"-n", "kube-system", "get", "pods"}, "get", 2},
		{"kubectl long flags", []string{"--context", "prod", "--namespace=x", "get", "pods"}, "get", 3},
		{"boolean flag", []string{"--insecure-skip-tls-verify", "get", "pods"}, "get", 1},
		{"mixed with kubeconfig", []string{"--kubeconfig", "/p", "-n", "ns", "images"}, "images", 4},
		{"no args", nil, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, idx := firstCommand(tt.args)
			if cmd != tt.wantCmd || idx != tt.wantIndex {
				t.Errorf("firstCommand(%v) = (%q, %d), want (%q, %d)", tt.args, cmd, idx, tt.wantCmd, tt.wantIndex)
			}
		})
	}
}

// A --kubeconfig value that happens to look like a subcommand must not be
// mistaken for one.
func TestFirstCommandIgnoresFlagValues(t *testing.T) {
	cmd, idx := firstCommand([]string{"--kubeconfig", "get", "pods"})
	if cmd != "pods" || idx != 2 {
		t.Errorf(`firstCommand = (%q, %d), want ("pods", 2)`, cmd, idx)
	}
}

func TestSplitKubeconfigFlag(t *testing.T) {
	tests := []struct {
		args     []string
		wantPath string
		wantRest []string
	}{
		{[]string{"--kubeconfig", "/p"}, "/p", []string{}},
		{[]string{"--kubeconfig=/p"}, "/p", []string{}},
		{[]string{}, "", []string{}},
		{[]string{"--other"}, "", []string{"--other"}},
		{[]string{"--kubeconfig"}, "", []string{}}, // dangling flag, no value
		{[]string{"-n", "ns", "--kubeconfig", "/p", "--context", "c"}, "/p", []string{"-n", "ns", "--context", "c"}},
	}
	for _, tt := range tests {
		path, rest := splitKubeconfigFlag(tt.args)
		if path != tt.wantPath || !slices.Equal(rest, tt.wantRest) {
			t.Errorf("splitKubeconfigFlag(%v) = (%q, %v), want (%q, %v)", tt.args, path, rest, tt.wantPath, tt.wantRest)
		}
	}
}

func TestKelperSubcommandsIncludeCompletionCallbacks(t *testing.T) {
	// The generated shell scripts call these; if they are not routed to cobra,
	// completion silently falls through to kubectl.
	for _, name := range []string{"__complete", "__completeNoDesc"} {
		if !kelperSubcommands[name] {
			t.Errorf("%s must be handled by kelper, not forwarded to kubectl", name)
		}
	}
}

func TestIsYAMLOutput(t *testing.T) {
	yes := [][]string{{"get", "pods", "-o", "yaml"}, {"get", "pods", "-oyaml"}}
	for _, args := range yes {
		if !isYAMLOutput(args) {
			t.Errorf("expected %v to be YAML output", args)
		}
	}
	if isYAMLOutput([]string{"get", "pods", "-o", "json"}) {
		t.Error("expected -o json not to be YAML output")
	}
}
