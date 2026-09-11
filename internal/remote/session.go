// Package remote keeps one shell open on a machine and asks it questions.
//
// It exists so that the file browser can list a directory in the time it takes
// to press a key, rather than in the time it takes to open an ssh connection.
// One connection is made when the browser opens and every listing after that
// travels down it.
//
// What this package does not do is speak SSH. tram does not embed an ssh
// client, here or anywhere else: host key checking, ProxyJump chains, agents,
// certificates and everything else in ssh_config belong to ssh, which already
// does them properly. This is ssh with a shell on the far end and a marker to
// tell one answer from the next.
package remote

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Session is a shell running on one host.
type Session struct {
	Host string

	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	mark string

	mu      sync.Mutex
	closed  bool
	timeout time.Duration
}

// Options carry what the command layer knows and this package must not guess.
type Options struct {
	// Argv is the whole ssh command line, built by the launcher so that this
	// session is opened exactly the way an interactive one would be.
	Argv []string
	// Env is the environment ssh runs with, which carries the askpass helper.
	Env []string
	// Timeout bounds one request. It is generous: the first one includes the
	// whole connection, jump stations and all.
	Timeout time.Duration
}

// preamble is what the shell is told before anything is asked of it.
//
// Sending the error stream into the answer stream is the whole trick. The two
// are separate channels in the SSH protocol, so a message written to one can
// overtake output written to the other, and a listing would arrive split in
// half by an error about something else. Joined, they are one ordered stream.
const preamble = "exec 2>&1\n"

// Open starts a shell on the far end and waits for it to answer.
func Open(host string, opt Options) (*Session, error) {
	if len(opt.Argv) == 0 {
		return nil, fmt.Errorf("no ssh command to run")
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 30 * time.Second
	}

	cmd := exec.Command(opt.Argv[0], opt.Argv[1:]...)
	cmd.Env = opt.Env

	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// ssh's own complaints arrive on its error stream, and they are the ones
	// that say why a connection did not open. The same pipe takes both, so a
	// failure to connect is an answer rather than a silence.
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ssh: %w", err)
	}

	s := &Session{
		Host:    host,
		cmd:     cmd,
		in:      in,
		out:     bufio.NewReaderSize(out, 64*1024),
		mark:    "__tram_" + nonce() + "__",
		timeout: opt.Timeout,
	}
	if _, err := in.Write([]byte(preamble)); err != nil {
		s.Close()
		return nil, err
	}
	// Nothing is asked of the connection until it has said hello, so that a
	// refused or misconfigured host fails here, with ssh's own words, rather
	// than halfway through drawing a file list.
	if _, _, err := s.Run("true"); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Run sends one command and returns its output and exit status.
//
// Every command is followed by a marker carrying the status. The marker is what
// separates one answer from the next: a shell reading from a pipe has no
// prompt, so without it there is no way to know that a listing has ended rather
// than paused.
func (s *Session) Run(command string) (lines []string, status int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, 0, fmt.Errorf("the connection to %s is closed", s.Host)
	}

	if _, err := fmt.Fprintf(s.in, "%s\necho %s$?\n", command, s.mark); err != nil {
		return nil, 0, s.died(err)
	}

	type answer struct {
		lines  []string
		status int
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		var got []string
		for {
			line, err := s.out.ReadString('\n')
			if i := strings.Index(line, s.mark); i >= 0 {
				code, _ := strconv.Atoi(strings.TrimSpace(line[i+len(s.mark):]))
				if head := strings.TrimRight(line[:i], "\r"); head != "" {
					got = append(got, head)
				}
				done <- answer{got, code, nil}
				return
			}
			if line != "" {
				got = append(got, strings.TrimRight(line, "\r\n"))
			}
			if err != nil {
				done <- answer{got, 0, fmt.Errorf("the connection to %s ended: %s",
					s.Host, firstComplaint(got))}
				return
			}
		}
	}()

	select {
	case a := <-done:
		if a.err != nil {
			return a.lines, 0, s.died(a.err)
		}
		return a.lines, a.status, nil
	case <-time.After(s.timeout):
		// The reader is left running on a session that is about to be closed,
		// which ends it: killing the process closes the pipe it is blocked on.
		return nil, 0, s.died(fmt.Errorf("%s did not answer in %s", s.Host, s.timeout))
	}
}

// died marks the session unusable and returns the error that killed it.
func (s *Session) died(err error) error {
	s.closed = true
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	return err
}

// Close ends the shell and the connection under it.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.in != nil {
		_, _ = s.in.Write([]byte("exit\n"))
		_ = s.in.Close()
	}
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	// A moment to leave politely, then not.
	done := make(chan struct{})
	go func() { _, _ = s.cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = s.cmd.Process.Kill()
	}
	return nil
}

// firstComplaint picks the line worth showing out of whatever came back before
// a connection died, which is usually ssh explaining itself.
func firstComplaint(lines []string) string {
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		return l
	}
	return "no answer"
}

func nonce() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

// Quote wraps a path for a POSIX shell.
//
// Single quotes, because inside them the shell interprets nothing at all. A
// single quote in the name itself is the one character that has to leave the
// quoting and come back, which is what the replacement does.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
