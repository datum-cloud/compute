package compute

import (
	"io"
	"testing"
)

// TestGroupCommandsRejectStrayVerbs checks the real command tree: a mistyped
// verb must fail loudly instead of falling through to the group's listing,
// which once let `workloads destroy <name>` print a table and delete nothing.
func TestGroupCommandsRejectStrayVerbs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "workloads destroy",
			args: []string{"workloads", "destroy", "hello-nginx"},
			want: "unknown command \"destroy\" for \"datumctl compute workloads\"\n\nDid you mean: datumctl compute destroy hello-nginx",
		},
		{
			name: "instances restart",
			args: []string{"instances", "restart", "api"},
			want: "unknown command \"restart\" for \"datumctl compute instances\"\n\nDid you mean: datumctl compute restart api",
		},
		{
			name: "quota takes no arguments",
			args: []string{"quota", "workloads"},
			want: "unknown command \"workloads\" for \"datumctl compute quota\"\n\nDid you mean: datumctl compute workloads",
		},
		{
			name: "access takes no arguments",
			args: []string{"access", "grant"},
			want: "unknown command \"grant\" for \"datumctl compute access\"",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := Command()
			cmd.SetArgs(tc.args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)

			// Argument validation runs before the activation gate, so this stays
			// offline and gives the same message to an unentitled project.
			err := cmd.Execute()
			if err == nil {
				t.Fatalf("Execute(%q) = nil, want error", tc.args)
			}
			if err.Error() != tc.want {
				t.Errorf("Execute(%q) error =\n%q\nwant\n%q", tc.args, err.Error(), tc.want)
			}
		})
	}
}
