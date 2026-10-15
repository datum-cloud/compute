package build

import (
	"errors"
	"strings"
)

// userError is a failure worded for the user. Its cause is only shown with
// --verbose.
type userError struct {
	message string
	cause   error
}

func (e *userError) Error() string { return e.message }
func (e *userError) Unwrap() error { return e.cause }

func paragraphs(p ...string) string { return strings.Join(p, "\n\n") }

// withErrorDetails appends a userError's underlying cause when verbose.
func withErrorDetails(verbose bool, err error) error {
	var ue *userError
	if verbose && errors.As(err, &ue) && ue.cause != nil {
		return &userError{message: ue.message + "\n\nDetails: " + ue.cause.Error(), cause: ue.cause}
	}
	return err
}
