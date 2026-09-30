package exec

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
	"go.datum.net/compute/internal/consoleclient"
	"go.datum.net/compute/internal/consolesession"
)

const exitInterrupted = 130

type options struct {
	instance  string
	container string
	command   []string
	stdin     bool
	tty       bool
}

func Command() *cobra.Command {
	return newCommand(run)
}

func newCommand(run func(*cobra.Command, *options) error) *cobra.Command {
	opts := &options{}

	cmd := &cobra.Command{
		Use:   "exec <instance> -- <command> [args...]",
		Short: "Run a command in an instance",
		Long: `Run a command in a container of a running instance, or open an interactive
shell in it.

The command's exit code becomes datumctl's exit code. When the platform ends the
session instead, such as when it expires, datumctl exits 1 and says why. Usage
errors exit 2.`,
		Example: `  # Open an interactive shell
  datumctl compute exec api-dfw-0 -it -- sh

  # Run a single command
  datumctl compute exec api-dfw-0 -- cat /etc/os-release

  # Choose a container in an instance that runs several
  datumctl compute exec api-dfw-0 -c worker -- ps aux`,
		Args: func(cmd *cobra.Command, args []string) error {
			instance, command, err := parseArgs(args, cmd.ArgsLenAtDash())
			if err != nil {
				return &util.ExitError{Code: 2, Err: err}
			}
			opts.instance, opts.command = instance, command
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd, opts)
		},
		ValidArgsFunction: util.CompleteInstanceNames,
	}
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &util.ExitError{Code: 2, Err: err}
	})

	cmd.Flags().StringVarP(&opts.container, "container", "c", "", "Container to run the command in; required when the instance runs several")
	cmd.Flags().BoolVarP(&opts.stdin, "stdin", "i", false, "Pass standard input to the command")
	cmd.Flags().BoolVarP(&opts.tty, "tty", "t", false, "Run the command in a terminal")

	return cmd
}

func parseArgs(args []string, dash int) (string, []string, error) {
	switch {
	case len(args) == 0:
		return "", nil, errors.New("name an instance and a command, e.g. 'datumctl compute exec <instance> -- sh'")
	case dash < 0:
		return "", nil, errors.New("separate the command with --, e.g. 'datumctl compute exec <instance> -- sh'")
	case dash != 1:
		return "", nil, errors.New("name exactly one instance before --")
	case len(args) == dash:
		return "", nil, errors.New("name a command after --")
	}
	return args[0], args[dash:], nil
}

func run(cmd *cobra.Command, opts *options) error {
	project := util.ProjectFromCmd(cmd)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	stdinFd := int(os.Stdin.Fd())
	tty := opts.tty
	if tty && !term.IsTerminal(stdinFd) {
		fmt.Fprintln(cmd.ErrOrStderr(), "Unable to use a terminal: standard input is not a terminal")
		tty = false
	}

	c, err := util.NewWatchClient(project)
	if err != nil {
		return err
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generating session key: %w", err)
	}

	session, err := createSession(ctx, c, opts, tty, consolesession.PublicKey(key))
	if err != nil {
		return err
	}
	defer deleteSession(project, session)

	session, err = waitReady(ctx, c, session, readyTimeout)
	if err != nil {
		return interrupted(ctx, err)
	}

	stream, err := consoleclient.Connect(ctx, key, string(session.UID), connectionOf(session))
	if err != nil {
		return interrupted(ctx, fmt.Errorf("connecting to the session: %w", err))
	}
	go func() {
		<-ctx.Done()
		_ = stream.Close()
	}()

	if tty && opts.stdin {
		state, err := term.MakeRaw(stdinFd)
		if err != nil {
			_ = stream.Close()
			return fmt.Errorf("setting up the terminal: %w", err)
		}
		defer func() { _ = term.Restore(stdinFd, state) }()
	}
	if tty {
		go forwardResizes(ctx, int(os.Stdout.Fd()), stream)
	}
	if opts.stdin {
		go func() {
			if _, err := io.Copy(stream, cmd.InOrStdin()); err == nil {
				_ = stream.CloseStdin()
			}
		}()
	}

	res, err := stream.Wait(cmd.OutOrStdout(), cmd.ErrOrStderr())
	if err != nil {
		if ctx.Err() != nil {
			return interrupted(ctx, err)
		}
		return exitFor(endingFromStatus(ctx, c, session, err))
	}
	return exitFor(res, nil)
}

// endingFromStatus explains a stream that broke without an ending by asking
// the API how the session ended, since the agent records that too.
func endingFromStatus(ctx context.Context, c client.Client, s *computev1alpha.InstanceConsoleSession, streamErr error) (consoleclient.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		if err := c.Get(ctx, client.ObjectKeyFromObject(s), s); err != nil {
			break
		}
		if res, ok := ending(s); ok {
			return res, nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(500 * time.Millisecond):
		}
	}
	return consoleclient.Result{}, fmt.Errorf("lost the connection to the session: %w", streamErr)
}

func interrupted(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return &util.ExitError{Code: exitInterrupted}
	}
	return err
}
