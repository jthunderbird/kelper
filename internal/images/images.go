package images

import (
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
)

func Command(cs **kubernetes.Clientset) *cobra.Command {
	return &cobra.Command{
		Use:     "images",
		Aliases: []string{"image", "imgs", "img"},
		Short:   "Show container images per pod",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("images: not yet implemented")
			return nil
		},
	}
}
