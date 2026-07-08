package main

import "testing"

func TestShouldTransformKubectlOutput(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{
			name: "get yaml short output flag",
			args: []string{"get", "deploy", "-n", "isp-north-america", "prov1", "-o", "yaml"},
			want: true,
		},
		{
			name: "get yaml joined short output flag",
			args: []string{"get", "secret", "root-ca", "-oyaml"},
			want: true,
		},
		{
			name: "get yaml long output flag",
			args: []string{"--namespace", "isp-north-america", "get", "deploy", "prov1", "--output=yaml"},
			want: true,
		},
		{
			name: "get table streams",
			args: []string{"get", "pods", "-A"},
			want: false,
		},
		{
			name: "get json streams",
			args: []string{"get", "pods", "-o", "json"},
			want: false,
		},
		{
			name: "edit streams",
			args: []string{"edit", "deploy", "-n", "isp-north-america", "prov1"},
			want: false,
		},
		{
			name: "exec streams",
			args: []string{"exec", "-it", "pod/prov1", "--", "sh"},
			want: false,
		},
		{
			name: "logs follow streams",
			args: []string{"logs", "-f", "deploy/prov1"},
			want: false,
		},
		{
			name: "watching yaml get streams",
			args: []string{"get", "pods", "-w", "-o", "yaml"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldTransformKubectlOutput(tt.args); got != tt.want {
				t.Fatalf("shouldTransformKubectlOutput(%q) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestKubectlVerb(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "plain verb",
			args: []string{"edit", "deploy", "prov1"},
			want: "edit",
		},
		{
			name: "skips global flags before verb",
			args: []string{"--context", "prod", "-n", "default", "get", "pods"},
			want: "get",
		},
		{
			name: "stops at separator",
			args: []string{"--", "get", "pods"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := kubectlVerb(tt.args); got != tt.want {
				t.Fatalf("kubectlVerb(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}
