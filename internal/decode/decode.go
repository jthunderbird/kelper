package decode

import (
	"fmt"
	"io"

	"k8s.io/client-go/kubernetes"
)

func Run(cs *kubernetes.Clientset, args []string) error {
	fmt.Println("decode: not yet implemented")
	return nil
}

func Print(w io.Writer, yamlBytes []byte) error {
	fmt.Fprintln(w, "decode: not yet implemented")
	return nil
}
