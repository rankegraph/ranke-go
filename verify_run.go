// package: ranke / verify
// type:    logic
// job:     the live handle on a verification — a Failure list and a progress count safe to read
// while the closure walk runs, plus the Wait that blocks until it is done
// limits:  holds no rules and walks nothing; the walk and the rule registry are verify.go's
package ranke

import "sync"

// Failure is one verification failure: the claim that failed, its depth in
// the walk, and why. It is an error itself, so it travels as the cause of one:
// errors.Is reaches the rule through Unwrap, errors.As recovers the claim and depth.
type Failure struct {
	ID    Id
	Depth int
	Err   error
}

// Error names the claim and the rule it broke.
func (f Failure) Error() string {
	id := "?"
	if f.ID != nil {
		id = f.ID.String()
	}
	if f.Err == nil {
		return "ranke.verify: claim " + id + " failed"
	}
	return "ranke.verify: claim " + id + ": " + f.Err.Error()
}

// Unwrap yields the rule that failed, which is what errors.Is matches.
func (f Failure) Unwrap() error { return f.Err }

// VerificationRun is a live handle on a verification, safe to read while the
// walk runs — poll for progress, or Wait for completion.
type VerificationRun interface {
	// Verified is the number of claims that passed so far.
	Verified() int
	// Failures is a snapshot of the failures found so far.
	Failures() []Failure
	// Done reports whether the walk has finished (completed or stopped).
	Done() bool
	// Err is a terminal error that aborted the walk (a load failure,
	// ctx cancellation) — distinct from per-claim Failures. Nil otherwise.
	Err() error
	// Wait blocks until the walk is Done.
	Wait()
}

type verificationRun struct {
	mu       sync.Mutex
	verified int
	failures []Failure
	done     bool
	err      error
	doneCh   chan struct{}
}

func newRun() *verificationRun { return &verificationRun{doneCh: make(chan struct{})} }

func (r *verificationRun) Verified() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.verified
}

func (r *verificationRun) Failures() []Failure {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Failure, len(r.failures))
	copy(out, r.failures)
	return out
}

func (r *verificationRun) Done() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.done
}

func (r *verificationRun) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *verificationRun) Wait() { <-r.doneCh }

func (r *verificationRun) pass() {
	r.mu.Lock()
	r.verified++
	r.mu.Unlock()
}

func (r *verificationRun) fail(f Failure, onError func(Failure)) int {
	r.mu.Lock()
	r.failures = append(r.failures, f)
	n := len(r.failures)
	r.mu.Unlock()
	if onError != nil {
		onError(f)
	}
	return n
}

func (r *verificationRun) abort(err error) {
	r.mu.Lock()
	r.err = err
	r.mu.Unlock()
}

func (r *verificationRun) finish() {
	r.mu.Lock()
	r.done = true
	r.mu.Unlock()
	close(r.doneCh)
}
