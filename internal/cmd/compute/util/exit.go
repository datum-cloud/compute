package util

import "strconv"

// ExitError ends the plugin with Code. Err, when set, is printed first; a nil
// Err exits silently, as when a remote command's own exit code is passed on.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return "exit status " + strconv.Itoa(e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }
