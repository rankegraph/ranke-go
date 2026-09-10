// package: keysource / logic
// type:    io
// job:     the one grammar an app resolves a key argument through — a file, an environment
// variable, stdin or a prompt — with the rules that keep the material off disk and off the
// command line
// limits:  yields bytes and reads nothing into a key; what a PEM means is the library's
// (-> ranke.ParseKeypair). Key-agnostic on purpose, so an age passphrase takes the same grammar
package keysource

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Kind is where material comes from.
type Kind int

const (
	KindFile   Kind = iota // a path, bare or file:-prefixed
	KindEnv                // env:NAME
	KindStdin              // stdin
	KindPrompt             // prompt, and only under WithTTY
)

const (
	filePrefix = "file:"
	envPrefix  = "env:"
	stdinSpec  = "stdin"
	promptSpec = "prompt"
)

// keyPerm is the widest mode a key file may carry: owner-only, as ssh demands.
const keyPerm = 0o077

var (
	ErrSpec       = errors.New("keysource: unrecognised key source")
	ErrInline     = errors.New("keysource: key material was passed where a source was expected, so it has reached the process table, the shell history and any CI log — treat this key as compromised and rotate it")
	ErrPermission = errors.New("keysource: key file is readable by others (chmod 600 it)")
	ErrNoTTY      = errors.New("keysource: a prompt needs keysource.WithTTY, which a server must not grant")
	ErrEmpty      = errors.New("keysource: the source held nothing")
	errRead       = errors.New("keysource.Spec.Read")
)

// Spec is a checked key source with nothing read yet, so a caller validates its argument
// at startup and fetches a rotating secret where it signs.
type Spec struct {
	Kind Kind
	Name string // the path, or the environment variable's name; empty otherwise
	tty  bool
}

// Option narrows or widens what Parse admits.
type Option func(*Spec)

// WithTTY permits the "prompt" spelling. Grant it from a command a person runs, never
// from a server: a prompt blocks until someone types.
func WithTTY() Option { return func(s *Spec) { s.tty = true } }

// Parse reads the grammar, touching no file and no terminal: a bare path or file:PATH
// for a mounted secret, env:NAME where the key must not reach disk, stdin for a pipeline
// already holding it, and prompt for a person, under WithTTY.
func Parse(spec string, opts ...Option) (Spec, error) {
	s := Spec{}
	for _, o := range opts {
		o(&s)
	}
	if spec == "" {
		return Spec{}, fmt.Errorf("%w: %q", ErrSpec, spec)
	}
	// Before anything else: material where a source belongs is already spent.
	if strings.Contains(spec, "-----BEGIN") || strings.ContainsAny(spec, "\r\n") {
		return Spec{}, ErrInline
	}
	switch {
	case spec == stdinSpec:
		s.Kind = KindStdin
	case spec == promptSpec:
		if !s.tty {
			return Spec{}, ErrNoTTY
		}
		s.Kind = KindPrompt
	case strings.HasPrefix(spec, envPrefix):
		name := strings.TrimPrefix(spec, envPrefix)
		if name == "" {
			return Spec{}, fmt.Errorf("%w: %q names no variable", ErrSpec, spec)
		}
		s.Kind, s.Name = KindEnv, name
	case strings.HasPrefix(spec, filePrefix):
		path := strings.TrimPrefix(spec, filePrefix)
		if path == "" {
			return Spec{}, fmt.Errorf("%w: %q names no file", ErrSpec, spec)
		}
		s.Kind, s.Name = KindFile, path
	default:
		// Scheme-shaped is a source this does not serve: read as a filename it would
		// fail as "no such file: evn:KEY", naming the wrong mistake.
		if i := strings.IndexAny(spec, ":/"); i >= 0 && spec[i] == ':' {
			return Spec{}, fmt.Errorf("%w: %q — for a path with a colon, say file:%s", ErrSpec, spec, spec)
		}
		s.Kind, s.Name = KindFile, spec
	}
	return s, nil
}

// Read fetches the material. in is where "stdin" reads from and where a prompt is
// answered, so a test drives both without a terminal.
func (s Spec) Read(in io.Reader) ([]byte, error) {
	var (
		out []byte
		err error
	)
	switch s.Kind {
	case KindFile:
		out, err = s.readFile()
	case KindEnv:
		v, ok := os.LookupEnv(s.Name)
		if !ok {
			return nil, fmt.Errorf("%w: %s is unset", errRead, s.Name)
		}
		out = []byte(v)
	case KindStdin:
		out, err = io.ReadAll(in)
	case KindPrompt:
		out, err = readPrompt(in)
	default:
		return nil, fmt.Errorf("%w: %d", ErrSpec, s.Kind)
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, ErrEmpty
	}
	return out, nil
}

// readFile reads the key, refusing one others can read. The mode is checked on the open
// handle rather than the path, so the file answered for is the file checked.
func (s Spec) readFile() ([]byte, error) {
	f, err := os.Open(s.Name)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRead, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRead, err)
	}
	if mode := info.Mode().Perm(); mode&keyPerm != 0 {
		return nil, fmt.Errorf("%w: %s is %04o", ErrPermission, s.Name, mode)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRead, err)
	}
	return b, nil
}

// readPrompt takes a pasted key, ECHOING it: a paste nobody can see is a paste nobody
// can check. It ends at the PEM footer, or at a blank line for material without one.
func readPrompt(in io.Reader) ([]byte, error) {
	fmt.Fprintln(os.Stderr, "paste the key, then a blank line:")
	var out bytes.Buffer
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" && out.Len() > 0 {
			break
		}
		out.WriteString(line)
		out.WriteByte('\n')
		if strings.HasPrefix(line, "-----END") {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", errRead, err)
	}
	return out.Bytes(), nil
}

// Load is Parse then Read — the whole of what an app does once, turning one command-line
// argument into key material.
func Load(spec string, in io.Reader, opts ...Option) ([]byte, error) {
	s, err := Parse(spec, opts...)
	if err != nil {
		return nil, err
	}
	return s.Read(in)
}
