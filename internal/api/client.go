// Package api is hermes-hands' client for the Hermes Agent API. Client
// holds the connection (base URL, key, retries) and the hooks of the running
// turn; its views say which part of the API a caller uses:
//
//	Runs()     submit a turn (Ask — the stream or the poll), its state, stop it
//	Sessions() list, read, title and fork the gateway's sessions
//	Server()   the preflight check, the gateway's version and capabilities
//
// transport.go is the shared HTTP plumbing (retries, errors, the trace),
// stream.go the SSE event stream, decode.go the lenient JSON reading.
package api

import (
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/Zesales/hermes-hands/internal/guard"
)

// retrySleep is bash's fixed `sleep 2` between failed POST / capability
// attempts (not the poll interval, not exponential).
const retrySleep = 2 * time.Second

// Client talks to one Hermes gateway.
type Client struct {
	HTTP         *http.Client
	BaseURL      string
	Key          string
	Profile      string
	SecretsPath  string
	AllowHTTP    bool
	Retries      int
	PollInterval time.Duration
	RunTimeout   time.Duration // whole poll loop ceiling (HERMES_API_RUN_TIMEOUT)
	Instructions string

	// Caps is filled by Check from /v1/capabilities so callers can gate
	// optional features (run_events_sse, session_compress, …).
	Caps Caps

	// OnRunStart, when set, is called with the run id as soon as POST /v1/runs
	// returns it — before polling. The REPL uses it so Ctrl-C can POST
	// /v1/runs/{id}/stop on the in-flight run.
	OnRunStart func(runID string)

	// StreamMode: "" / "auto" (stream when the gateway advertises
	// run_events_sse), "on", or "off". OnDelta receives answer-text chunks as
	// they arrive over the SSE stream.
	StreamMode string
	OnDelta    func(text string)

	// OnEvent, when set, sees the type of every SSE frame of a run. Any frame
	// is a sign of life — the REPL's silence watchdog bumps on it.
	OnEvent func(event string)

	// OnHermesTool, when set, is told about Hermes' OWN server-side tool calls
	// (SSE tool.started / approval.request) and what became of them: action
	// "" = a thinking-side tool (web_search, memory, cron, …), "server" = an
	// exec / file tool doing server-side work, "steered" = one aimed at the
	// operator's machine the run was redirected away from, "approved" /
	// "denied" = an approval request and the answer it got. Stream transport
	// only — poll mode carries no events.
	OnHermesTool func(tool, preview, action string)

	// Guard judges each of Hermes' own tool calls (tool.started): steer the run
	// back to the hands or not, and with which message (internal/guard). nil
	// is guard.Strict — every server-side exec / file call is steered.
	Guard func(tool, preview string) guard.Verdict

	// OnApproval answers an approval request of Hermes' own server-side tool:
	// "once" | "session" | "deny". nil denies.
	OnApproval func(tool, what string) string

	// Trace, when set, receives the /debug wire trace: every HTTP request
	// (method, path, status, duration), every run input and outcome, every
	// raw SSE frame, every guard action. Fields carry message text verbatim —
	// the sink must redact before it writes anything.
	Trace func(kind string, fields map[string]any)

	Warnf func(string, ...any)
	Vlogf func(string, ...any)
	Log   func(string, ...any)

	now func() time.Time
	rnd func() int

	// retryPause overrides the fixed 2s pause between failed attempts; 0 keeps
	// the bash default. Set only by tests.
	retryPause time.Duration
}

// Runs is the Runs API: submit a turn, follow it, its state, stop it.
func (c *Client) Runs() Runs { return Runs{c} }

// Sessions is the session endpoints: list, read, title, fork.
func (c *Client) Sessions() Sessions { return Sessions{c} }

// Server is the gateway itself: the preflight check and its version.
func (c *Client) Server() Server { return Server{c} }

// Caps is the parsed /v1/capabilities surface — used to gate features that a
// given Hermes build may not have (SSE run events, session compress, …).
type Caps struct {
	Model    string
	Features map[string]bool
}

// Has reports whether a feature flag is present and true.
func (c Caps) Has(f string) bool { return c.Features[f] }

// NewHTTPClient builds a client with curl-equivalent timeouts: a per-dial
// connect timeout and an overall per-request deadline.
func NewHTTPClient(connectTimeout, maxTime time.Duration) *http.Client {
	return &http.Client{
		Timeout: maxTime,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: connectTimeout}).DialContext,
			TLSHandshakeTimeout:   connectTimeout,
			ExpectContinueTimeout: time.Second,
			MaxIdleConns:          10,
			IdleConnTimeout:       90 * time.Second,
		},
	}
}

func (c *Client) warnf(f string, a ...any) { logOr(c.Warnf, "hermes-hands: WARNING: "+f, a...) }

func (c *Client) logf(f string, a ...any) { logOr(c.Log, "hermes-hands: "+f, a...) }

func (c *Client) vlogf(f string, a ...any) {
	if c.Vlogf != nil {
		c.Vlogf(f, a...)
	}
}

func logOr(fn func(string, ...any), f string, a ...any) {
	if fn != nil {
		fn(f, a...)
		return
	}
	fmt.Fprintf(os.Stderr, f+"\n", a...)
}

func (c *Client) nowFn() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *Client) rndFn() int {
	if c.rnd != nil {
		return c.rnd()
	}
	return rand.Intn(32768)
}
