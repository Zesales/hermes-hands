// Package shell is the persistent shell: one `bash --login` kept alive over
// pipes so cd, exported vars, and ~/.bashrc aliases/functions survive between
// calls in a turn. Commands are framed with a random mark line that also
// carries the exit code; a per-read (idle) timeout or EOF recycles the shell.
package shell

import (
	"bufio"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Shell is a lazily-started persistent bash. Zero value is not usable; call New.
type Shell struct {
	repoRoot string
	maxOut   int // byte cap on captured output (bash: 2 * HERMES_HANDS_MAX_OUTPUT)

	mu         sync.Mutex
	cmd        *exec.Cmd
	in         io.WriteCloser
	stdoutPipe *os.File
	out        *bufio.Reader
	pgid       int
	up         bool

	// killPgid mirrors pgid for Kill(), which a signal handler may call
	// without holding mu.
	killPgid atomic.Int64
}

type outcome int

const (
	outOK outcome = iota
	outWriteErr
	outReadErr
)

// New returns a shell rooted at repoRoot. maxOut is the already-doubled byte
// cap (bash's `HH_TOOL_MAX_OUT * 2`). The bash process starts on the first Run.
func New(repoRoot string, maxOut int) *Shell {
	return &Shell{repoRoot: repoRoot, maxOut: maxOut}
}

// Run ports hh_shell_raw: send cmd, read to the mark, return the captured
// output (all trailing newlines stripped) and the command's exit code. A write
// failure yields ("(shell not available)", 124); an idle timeout or EOF yields
// (partial + "\n[timed out after <n>s - shell was reset]", 124) and a fresh,
// re-primed, re-cd'd shell.
func (s *Shell) Run(cmd string, timeout time.Duration) (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.up {
		if err := s.start(); err != nil {
			return "(shell not available)", 124
		}
	}

	out, exit, oc := s.execOne(cmd, timeout)
	switch oc {
	case outOK:
		return out, exit
	case outWriteErr:
		s.stop()
		_ = s.start()
		return "(shell not available)", 124
	default: // outReadErr
		s.stop()
		_ = s.start()
		return out + fmt.Sprintf("\n[timed out after %ds - shell was reset]", int(timeout/time.Second)), 124
	}
}

// Stop ends the shell (bash hh_shell_stop, hardened to a process-group kill).
func (s *Shell) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stop()
}

// Kill force-kills the shell's process group. Safe to call from a signal
// handler without holding the lock; it unblocks an in-flight Run.
func (s *Shell) Kill() {
	if p := s.killPgid.Load(); p > 0 {
		killGroup(int(p))
	}
}

func (s *Shell) start() error {
	cmd := exec.Command("bash", "--login")
	setPgid(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	cmd.Stdout = pw // fold stderr into stdout, like `exec bash --login 2>&1`
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = pr.Close()
		_ = pw.Close()
		return err
	}
	_ = pw.Close() // the child holds its own copy now

	s.cmd = cmd
	s.in = stdin
	s.stdoutPipe = pr
	s.out = bufio.NewReaderSize(pr, 64*1024)
	s.pgid = cmd.Process.Pid // Setpgid => pgid == pid
	s.killPgid.Store(int64(s.pgid))
	s.up = true

	// priming: source ~/.bashrc, kill the prompt, then cd into the repo.
	// Output discarded; failures ignored (bash primes with `|| true`).
	s.execOne("shopt -s expand_aliases; [ -r ~/.bashrc ] && . ~/.bashrc; PROMPT_COMMAND=; PS1=", 15*time.Second)
	s.execOne("cd "+shQuote(s.repoRoot), 10*time.Second)
	return nil
}

func (s *Shell) stop() {
	if !s.up {
		return
	}
	_, _ = io.WriteString(s.in, "exit\n")
	_ = s.in.Close()
	if p := s.killPgid.Swap(0); p > 0 {
		killGroup(int(p))
	}
	if s.stdoutPipe != nil {
		_ = s.stdoutPipe.Close()
	}
	_ = s.cmd.Wait()
	s.up = false
}

// execOne writes one framed command and reads until the mark line.
func (s *Shell) execOne(cmd string, timeout time.Duration) (string, int, outcome) {
	mark := fmt.Sprintf("__HC_%d_%d%d__", os.Getpid(), rnd(), rnd())
	if _, err := io.WriteString(s.in, cmd+"\nprintf '\\n"+mark+" %s\\n' \"$?\"\n"); err != nil {
		return "", 124, outWriteErr
	}

	var buf []byte
	n := 0
	for {
		_ = s.stdoutPipe.SetReadDeadline(time.Now().Add(timeout))
		line, err := s.out.ReadString('\n')
		if err != nil {
			// Idle timeout (os.ErrDeadlineExceeded) or EOF: bash's read loop
			// simply exits here — a partial line, the mark line included, is
			// not processed — so recycle the shell.
			return strings.TrimRight(string(buf), "\n"), 124, outReadErr
		}
		trimmed := line[:len(line)-1] // err==nil guarantees a trailing '\n'
		if rest, ok := strings.CutPrefix(trimmed, mark+" "); ok {
			exit, _ := strconv.Atoi(strings.TrimSpace(rest))
			return strings.TrimRight(string(buf), "\n"), exit, outOK
		}
		if n < s.maxOut {
			buf = append(buf, line...)
			n += len(line)
		}
	}
}

func rnd() int { return rand.Intn(32768) }

// shQuote single-quotes s for a bash `cd` (bash used `printf %q`).
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
