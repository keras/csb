package csb

import "fmt"

// ExitError carries a process exit status up to main instead of calling
// os.Exit here, so deferred cleanup still runs and the code stays testable.
// Err is nil when the status speaks for itself (a container or editor's own
// code, a --help screen), and main exits silently; otherwise main reports Err
// but exits with Code rather than 1.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("exit status %d", e.Code)
}

func (e *ExitError) Unwrap() error { return e.Err }
