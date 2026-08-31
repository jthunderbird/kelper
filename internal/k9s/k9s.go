// Package k9s embeds the k9s terminal UI so kelper can launch it directly.
//
// k9s is licensed under the Apache License 2.0; see the NOTICE file.
package k9s

import (
	"os"

	k9scmd "github.com/derailed/k9s/cmd"
)

// Run launches k9s with the given arguments.
//
// k9s parses os.Args through its own cobra root rather than accepting an
// argument slice, so os.Args is rewritten to look like a direct k9s invocation
// before handing control over. Without this, k9s sees kelper's "k9s"
// subcommand token as one of its own arguments and rejects it.
func Run(args []string) {
	os.Args = append([]string{"k9s"}, args...)
	k9scmd.Execute()
}
