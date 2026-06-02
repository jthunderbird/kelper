package healthcheck

import (
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
)

func Command(cs **kubernetes.Clientset) *cobra.Command {
	return &cobra.Command{
		Use:     "healthcheck",
		Aliases: []string{"health"},
		Short:   "Check cluster health",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("healthcheck: not yet implemented")
			return nil
		},
	}
}
