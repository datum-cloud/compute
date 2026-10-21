// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolExecSuggest formats a command a person can review and run themselves.
// It never creates a session or implies that the person has permission to do so.
const ToolExecSuggest = "compute_exec_suggest"

type ExecSuggestInput struct {
	Instance  string   `json:"instance" jsonschema:"Name of an existing Instance in the current project."`
	Container string   `json:"container,omitempty" jsonschema:"Container name. Required when the Instance has several containers."`
	Command   []string `json:"command" jsonschema:"Exact executable and arguments to suggest. One array element per argument; do not submit a shell command string."`
	TTL       string   `json:"ttl,omitempty" jsonschema:"Optional session time limit from 1m to 1h, for example 5m."`
}

type ExecSuggestOutput struct {
	Instance  string `json:"instance"`
	Container string `json:"container"`
	Command   string `json:"command"`
	Note      string `json:"note"`
}

func registerExecSuggestTool(s *mcp.Server, deps DepsFor) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        ToolExecSuggest,
		Annotations: readOnly(),
		Title:       "Suggest an instance command",
		Description: "Check an Instance and its container in the current project, then format an exact " +
			"datumctl compute exec command for the person to inspect and run themselves. " +
			"Use when an inside-the-container check would clarify a diagnosis. " +
			"Never claim to have run the command. Read-only.",
	}, execSuggest(deps))
}

func execSuggest(deps DepsFor) mcp.ToolHandlerFor[ExecSuggestInput, ExecSuggestOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ExecSuggestInput) (*mcp.CallToolResult, ExecSuggestOutput, error) {
		var out ExecSuggestOutput
		if in.Instance == "" {
			return nil, out, fmt.Errorf("name the Instance to check")
		}
		if err := validateExecArgs(in.Command); err != nil {
			return nil, out, err
		}
		if in.TTL != "" {
			ttl, err := time.ParseDuration(in.TTL)
			if err != nil || ttl < time.Minute || ttl > time.Hour {
				return nil, out, fmt.Errorf("ttl must be a duration from 1m to 1h")
			}
		}
		d, err := deps(ctx)
		if err != nil {
			return nil, out, err
		}
		if d.Reader == nil {
			return nil, out, fmt.Errorf("the Instance reader is unavailable")
		}
		instance, err := d.Reader.GetInstance(ctx, d.Namespace, in.Instance)
		if err != nil {
			return nil, out, err
		}
		if instance.Spec.Runtime.Sandbox == nil {
			return nil, out, fmt.Errorf("instance %q has no container to run a command in", in.Instance)
		}
		containers := instance.Spec.Runtime.Sandbox.Containers
		if len(containers) == 0 {
			return nil, out, fmt.Errorf("instance %q has no container to run a command in", in.Instance)
		}
		container := in.Container
		if container == "" {
			if len(containers) != 1 {
				return nil, out, fmt.Errorf("instance %q has several containers; name one", in.Instance)
			}
			container = containers[0].Name
		} else {
			found := false
			for _, c := range containers {
				if c.Name == container {
					found = true
					break
				}
			}
			if !found {
				return nil, out, fmt.Errorf("instance %q has no container %q", in.Instance, container)
			}
		}

		parts := []string{"datumctl", "compute", "exec", instance.Name, "-c", container}
		if in.TTL != "" {
			parts = append(parts, "--ttl", in.TTL)
		}
		parts = append(parts, "--")
		parts = append(parts, in.Command...)
		for i := range parts {
			parts[i] = quoteShellArg(parts[i])
		}
		return nil, ExecSuggestOutput{
			Instance:  instance.Name,
			Container: container,
			Command:   strings.Join(parts, " "),
			Note:      "This command has not run. The person must review and run it; Datum checks their shell permission and project session limit when they do.",
		}, nil
	}
}

func validateExecArgs(args []string) error {
	if len(args) == 0 || len(args) > 64 {
		return fmt.Errorf("command must contain an executable and at most 64 arguments")
	}
	total := 0
	for _, arg := range args {
		if strings.ContainsAny(arg, "\x00\r\n") {
			return fmt.Errorf("command arguments cannot contain a NUL or line break")
		}
		total += len(arg)
	}
	if args[0] == "" || total > 16*1024 {
		return fmt.Errorf("command needs an executable and at most 16 KiB of arguments")
	}
	return nil
}

func quoteShellArg(arg string) string {
	if arg != "" && strings.IndexFunc(arg, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') && !strings.ContainsRune("_@%+=:,./-", r)
	}) == -1 {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
}
