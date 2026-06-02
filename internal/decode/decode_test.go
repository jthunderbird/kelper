package decode_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jthunderbird/kelper/internal/decode"
)

const secretYAML = `
apiVersion: v1
kind: Secret
metadata:
  name: my-secret
  namespace: default
data:
  username: YWRtaW4=
  password: cHJvbS1vcGVyYXRvcg==
`

const secretListYAML = `
apiVersion: v1
kind: List
items:
- apiVersion: v1
  kind: Secret
  metadata:
    name: secret-one
    namespace: default
  data:
    key: dmFsdWU=
`

func TestDecodeSecretValues(t *testing.T) {
	var buf bytes.Buffer
	if err := decode.Print(&buf, []byte(secretYAML)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "admin") {
		t.Errorf("expected decoded username 'admin' in output, got: %s", out)
	}
	if !strings.Contains(out, "prom-operator") {
		t.Errorf("expected decoded password 'prom-operator' in output, got: %s", out)
	}
}

func TestDecodeSecretKeyLabels(t *testing.T) {
	var buf bytes.Buffer
	if err := decode.Print(&buf, []byte(secretYAML)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "KEY:") {
		t.Errorf("expected KEY: label in output, got: %s", out)
	}
}

func TestDecodeSecretList(t *testing.T) {
	var buf bytes.Buffer
	if err := decode.Print(&buf, []byte(secretListYAML)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "value") {
		t.Errorf("expected decoded value 'value' in output, got: %s", out)
	}
}

func TestDecodeNoBase64PaddingInOutput(t *testing.T) {
	var buf bytes.Buffer
	if err := decode.Print(&buf, []byte(secretYAML)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "YWRtaW4=") {
		t.Errorf("raw base64 should not appear in decoded output")
	}
}
