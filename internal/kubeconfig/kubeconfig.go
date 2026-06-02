package kubeconfig

import (
	"fmt"

	"github.com/spf13/cobra"
)

func Command() *cobra.Command {
	root := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Generate kubeconfig files",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("kubeconfig: not yet implemented")
			return nil
		},
	}
	return root
}
