package util

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// NoPositionalArgs rejects stray words on a command that takes none.
//
// Cobra's default argument validator accepts arbitrary positional arguments on
// any command that has a parent, so a listing command silently ignored the rest
// of the line: `workloads destroy api` printed the workload table and destroyed
// nothing, with no hint that half the command had been dropped. When the stray
// word names a real command elsewhere in the tree, the error points at the
// invocation the user meant.
func NoPositionalArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}

	msg := fmt.Sprintf("unknown command %q for %q", args[0], invocation(cmd))
	if match := findCommand(cmd.Root(), args[0]); match != nil {
		msg += fmt.Sprintf("\n\nDid you mean: %s", strings.Join(append([]string{invocation(match)}, args[1:]...), " "))
	}
	return errors.New(msg)
}

// invocation renders how a user types the command. The plugin's root command is
// named "compute", so cobra's own command path omits the datumctl it is always
// run through.
func invocation(cmd *cobra.Command) string {
	return "datumctl " + cmd.CommandPath()
}

// findCommand looks for a command named (or aliased) name anywhere below root,
// shallowest first so a top-level verb wins over a nested one of the same name.
func findCommand(root *cobra.Command, name string) *cobra.Command {
	level := []*cobra.Command{root}
	for len(level) > 0 {
		var next []*cobra.Command
		for _, cmd := range level {
			for _, child := range cmd.Commands() {
				if child.Name() == name || child.HasAlias(name) {
					return child
				}
				next = append(next, child)
			}
		}
		level = next
	}
	return nil
}
