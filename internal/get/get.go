package get

import (
	"fmt"

	"k8s.io/client-go/kubernetes"
)

func Run(cs *kubernetes.Clientset, args []string) error {
	fmt.Println("get: not yet implemented")
	return nil
}
