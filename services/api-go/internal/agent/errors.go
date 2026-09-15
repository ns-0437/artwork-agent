package agent

import "errors"

// ClassifiedError distinguishes failures worth retrying from ones that
// won't get better on retry (bad auth, a malformed request, an unknown
// model) - a network timeout or the model briefly failing to call the
// required tool are transient; a 401 or a 400 that isn't that specific
// "didn't call a tool" case is not, and retrying it just burns the retry
// budget on something guaranteed to fail again.
type ClassifiedError struct {
	Transient bool
	Err       error
}

func (e *ClassifiedError) Error() string { return e.Err.Error() }
func (e *ClassifiedError) Unwrap() error { return e.Err }

func transientErr(err error) error {
	return &ClassifiedError{Transient: true, Err: err}
}

func permanentErr(err error) error {
	return &ClassifiedError{Transient: false, Err: err}
}

// IsTransient reports whether err is worth retrying. An error that was
// never classified (e.g. a bug in our own request/response handling) is
// treated as NOT transient - retrying an unclassified error risks looping
// on something retrying can't fix.
func IsTransient(err error) bool {
	var ce *ClassifiedError
	if errors.As(err, &ce) {
		return ce.Transient
	}
	return false
}
