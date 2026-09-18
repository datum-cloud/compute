package util

import (
	"testing"

	"github.com/spf13/cobra"
)

func argsTestTree() *cobra.Command {
	root := &cobra.Command{Use: "compute"}
	workloads := &cobra.Command{Use: "workloads", Args: NoPositionalArgs, RunE: func(*cobra.Command, []string) error { return nil }}
	workloads.AddCommand(&cobra.Command{Use: "describe", Args: cobra.ExactArgs(1)})
	root.AddCommand(workloads)
	root.AddCommand(&cobra.Command{Use: "destroy", Args: cobra.ExactArgs(1)})
	root.AddCommand(&cobra.Command{Use: "build", Aliases: []string{"b"}})
	return root
}

func TestNoPositionalArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "no args is valid",
			args: nil,
		},
		{
			name: "stray word naming a sibling command suggests it",
			args: []string{"destroy", "hello-nginx"},
			want: "unknown command \"destroy\" for \"datumctl compute workloads\"\n\nDid you mean: datumctl compute destroy hello-nginx",
		},
		{
			name: "stray word matching an alias suggests the command",
			args: []string{"b"},
			want: "unknown command \"b\" for \"datumctl compute workloads\"\n\nDid you mean: datumctl compute build",
		},
		{
			name: "stray word naming nothing is still rejected",
			args: []string{"list"},
			want: "unknown command \"list\" for \"datumctl compute workloads\"",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workloads, _, err := argsTestTree().Find([]string{"workloads"})
			if err != nil {
				t.Fatalf("Find(workloads) = %v", err)
			}
			err = NoPositionalArgs(workloads, tc.args)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("NoPositionalArgs(%q) = %v, want nil", tc.args, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("NoPositionalArgs(%q) = nil, want error", tc.args)
			}
			if err.Error() != tc.want {
				t.Errorf("NoPositionalArgs(%q) error =\n%q\nwant\n%q", tc.args, err.Error(), tc.want)
			}
		})
	}
}

// TestNoPositionalArgsKeepsSubcommands guards against the validator swallowing
// a real subcommand invocation, which cobra dispatches before validating args.
func TestNoPositionalArgsKeepsSubcommands(t *testing.T) {
	cmd, args, err := argsTestTree().Find([]string{"workloads", "describe", "api"})
	if err != nil {
		t.Fatalf("Find = %v", err)
	}
	if cmd.Name() != "describe" {
		t.Fatalf("resolved command = %q, want %q", cmd.Name(), "describe")
	}
	if err := cmd.ValidateArgs(args); err != nil {
		t.Errorf("ValidateArgs(%q) = %v, want nil", args, err)
	}
}
