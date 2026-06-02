package resources

import (
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
)

func Command(cs **kubernetes.Clientset) *cobra.Command {
	return &cobra.Command{
		Use:     "resources",
		Aliases: []string{"resource", "res"},
		Short:   "Show resource limits and requests per pod",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("resources: not yet implemented")
			return nil
		},
	}
}
