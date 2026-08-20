package util

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestGateExempt(t *testing.T) {
	root := &cobra.Command{Use: "compute"}

	access := &cobra.Command{Use: "access"}
	MarkGateExempt(access)
	request := &cobra.Command{Use: "request"}
	access.AddCommand(request)

	instances := &cobra.Command{Use: "instances"}
	completion := &cobra.Command{Use: "completion"}
	bash := &cobra.Command{Use: "bash"}
	completion.AddCommand(bash)
	help := &cobra.Command{Use: "help"}
	complete := &cobra.Command{Use: cobra.ShellCompRequestCmd}

	root.AddCommand(access, instances, completion, help, complete)

	tests := []struct {
		name string
		cmd  *cobra.Command
		want bool
	}{
		{"access is exempt", access, true},
		{"access request inherits exemption", request, true},
		{"completion is exempt", completion, true},
		{"completion subcommand is exempt", bash, true},
		{"help is exempt", help, true},
		{"__complete is exempt", complete, true},
		{"a data command is gated", instances, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := GateExempt(tc.cmd); got != tc.want {
				t.Fatalf("GateExempt(%s) = %v, want %v", tc.cmd.Name(), got, tc.want)
			}
		})
	}
}
