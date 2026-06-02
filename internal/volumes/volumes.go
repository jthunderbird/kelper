package volumes

import (
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
)

func Command(cs **kubernetes.Clientset) *cobra.Command {
	return &cobra.Command{
		Use:     "volumes",
		Aliases: []string{"volume", "vols", "vol"},
		Short:   "Show volume mounts per pod",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("volumes: not yet implemented")
			return nil
		},
	}
}
