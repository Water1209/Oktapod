// Package modeltransport owns the bounded HTTP phases shared by model
// adapters. It deliberately keeps credentials and provider payloads out of
// errors while preserving enough typed detail for durable diagnostics.
package modeltransport

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"
)

const (
	DialTimeout           = 10 * time.Second
	TLSHandshakeTimeout   = 10 * time.Second
	ResponseHeaderTimeout = 60 * time.Second
	StreamIdleTimeout     = 60 * time.Second
)

type Timeouts struct {
	FirstToken time.Duration
	StreamIdle time.Duration
}

func DefaultTimeouts() Timeouts {
	return Timeouts{FirstToken: 15 * time.Second, StreamIdle: StreamIdleTimeout}
}

func (timeouts Timeouts) Normalized() Timeouts {
	if timeouts.FirstToken <= 0 {
		timeouts.FirstToken = DefaultTimeouts().FirstToken
	}
	if timeouts.StreamIdle <= 0 {
		timeouts.StreamIdle = StreamIdleTimeout
	}
	return timeouts
}

func Remaining(total time.Duration, started time.Time) time.Duration {
	if total <= 0 {
		return 0
	}
	remaining := total - time.Since(started)
	if remaining <= 0 {
		return time.Nanosecond
	}
	return remaining
}

const (
	KindDNS               = "dns_error"
	KindConnect           = "connect_error"
	KindTLS               = "tls_error"
	KindResponseTimeout   = "response_header_timeout"
	KindFirstTokenTimeout = "first_token_timeout"
	KindStreamIdleTimeout = "stream_idle_timeout"
	KindStreamInterrupted = "stream_interrupted"
	KindNetwork           = "network_error"
)

const (
	PhaseRequest         = "request"
	PhaseDNS             = "dns"
	PhaseConnect         = "connect"
	PhaseTLS             = "tls_handshake"
	PhaseWriteRequest    = "write_request"
	PhaseResponseHeaders = "response_headers"
	PhaseResponseBody    = "response_body"
)

// Failure is safe to persist or show to users: Error never includes the URL,
// request body, provider response body, or the wrapped transport text.
type Failure struct {
	Kind     string
	Phase    string
	TimedOut bool
	Cause    error
}

func (f *Failure) Error() string {
	if f == nil {
		return "model transport failed"
	}
	if f.TimedOut {
		return fmt.Sprintf("model transport timeout during %s", f.Phase)
	}
	return fmt.Sprintf("model transport failed during %s", f.Phase)
}

func (f *Failure) Unwrap() error { return f.Cause }

func (f *Failure) ModelFailureKind() string  { return f.Kind }
func (f *Failure) ModelFailurePhase() string { return f.Phase }
func (f *Failure) ModelFailureTimeout() bool { return f.TimedOut }

// NewHTTPClient uses phase deadlines instead of http.Client.Timeout. A total
// client timeout also counts time spent reading a healthy SSE stream and can
// therefore abort an actively progressing model response.
func NewHTTPClient() *http.Client {
	return NewHTTPClientWithTimeouts(DefaultTimeouts())
}

func NewHTTPClientWithTimeouts(timeouts Timeouts) *http.Client {
	timeouts = timeouts.Normalized()
	base, ok := http.DefaultTransport.(*http.Transport)
	var transport *http.Transport
	if ok {
		transport = base.Clone()
	} else {
		transport = &http.Transport{}
	}
	dialer := &net.Dialer{Timeout: DialTimeout, KeepAlive: 30 * time.Second}
	transport.DialContext = dialer.DialContext
	transport.TLSHandshakeTimeout = TLSHandshakeTimeout
	// Receiving headers is necessary but not sufficient for a streaming model
	// response. Bound this phase by the end-to-end first-token deadline.
	transport.ResponseHeaderTimeout = timeouts.FirstToken
	transport.IdleConnTimeout = 90 * time.Second
	transport.ExpectContinueTimeout = time.Second
	return &http.Client{Transport: transport}
}

type requestTrace struct {
	mu    sync.Mutex
	phase string
}

func (t *requestTrace) set(phase string) {
	t.mu.Lock()
	t.phase = phase
	t.mu.Unlock()
}

func (t *requestTrace) get() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.phase == "" {
		return PhaseRequest
	}
	return t.phase
}

// Do attaches a request-local trace and classifies failures by the last HTTP
// phase reached. The returned error is safe and still unwraps to the original
// cause for errors.Is/errors.As.
func Do(client *http.Client, request *http.Request) (*http.Response, error) {
	trace := &requestTrace{phase: PhaseRequest}
	clientTrace := &httptrace.ClientTrace{
		DNSStart:             func(httptrace.DNSStartInfo) { trace.set(PhaseDNS) },
		ConnectStart:         func(_, _ string) { trace.set(PhaseConnect) },
		TLSHandshakeStart:    func() { trace.set(PhaseTLS) },
		GotConn:              func(httptrace.GotConnInfo) { trace.set(PhaseWriteRequest) },
		WroteRequest:         func(httptrace.WroteRequestInfo) { trace.set(PhaseResponseHeaders) },
		GotFirstResponseByte: func() { trace.set(PhaseResponseBody) },
	}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), clientTrace))
	response, err := client.Do(request)
	if err == nil {
		return response, nil
	}
	return nil, classify(err, trace.get())
}

func classify(err error, phase string) *Failure {
	timedOut := errors.Is(err, context.DeadlineExceeded)
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		timedOut = true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) || phase == PhaseDNS {
		return &Failure{Kind: KindDNS, Phase: PhaseDNS, TimedOut: timedOut, Cause: err}
	}
	switch phase {
	case PhaseConnect:
		return &Failure{Kind: KindConnect, Phase: phase, TimedOut: timedOut, Cause: err}
	case PhaseTLS:
		return &Failure{Kind: KindTLS, Phase: phase, TimedOut: timedOut, Cause: err}
	case PhaseResponseHeaders:
		kind := KindNetwork
		if timedOut {
			kind = KindResponseTimeout
		}
		return &Failure{Kind: kind, Phase: phase, TimedOut: timedOut, Cause: err}
	default:
		return &Failure{Kind: KindNetwork, Phase: phase, TimedOut: timedOut, Cause: err}
	}
}

type scanItem struct {
	line string
	done bool
	err  error
}

// ScanLines consumes a streaming response with an inactivity deadline that is
// reset by every received line, including provider keep-alives. Closing body on
// timeout unblocks Scanner so no detached read remains after the call returns.
func ScanLines(ctx context.Context, body io.ReadCloser, reader io.Reader, maxToken int, idle time.Duration, handle func(string) error) error {
	return ScanLinesWithProgress(ctx, body, reader, maxToken, 0, idle, func(line string) (bool, error) {
		return true, handle(line)
	})
}

// ScanLinesWithProgress distinguishes a real model token from SSE keep-alives.
// firstProgress bounds time to the first content, reasoning, or tool-call
// delta; idle is reset by every received line after transport begins.
func ScanLinesWithProgress(ctx context.Context, body io.ReadCloser, reader io.Reader, maxToken int, firstProgress, idle time.Duration, handle func(string) (bool, error)) error {
	if idle <= 0 {
		idle = StreamIdleTimeout
	}
	if maxToken <= 0 {
		maxToken = 2 << 20
	}
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	items := make(chan scanItem, 1)
	go func() {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64<<10), maxToken)
		for scanner.Scan() {
			select {
			case items <- scanItem{line: scanner.Text()}:
			case <-scanCtx.Done():
				return
			}
		}
		select {
		case items <- scanItem{done: true, err: scanner.Err()}:
		case <-scanCtx.Done():
		}
	}()

	timer := time.NewTimer(idle)
	defer timer.Stop()
	var firstTimer *time.Timer
	var firstTimerC <-chan time.Time
	if firstProgress > 0 {
		firstTimer = time.NewTimer(firstProgress)
		firstTimerC = firstTimer.C
		defer firstTimer.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			_ = body.Close()
			return ctx.Err()
		case <-timer.C:
			cancel()
			_ = body.Close()
			return &Failure{Kind: KindStreamIdleTimeout, Phase: PhaseResponseBody, TimedOut: true}
		case <-firstTimerC:
			cancel()
			_ = body.Close()
			return &Failure{Kind: KindFirstTokenTimeout, Phase: PhaseResponseBody, TimedOut: true}
		case item := <-items:
			if item.done {
				if item.err != nil {
					return &Failure{Kind: KindStreamInterrupted, Phase: PhaseResponseBody, Cause: item.err}
				}
				return nil
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idle)
			progressed, err := handle(item.line)
			if err != nil {
				cancel()
				_ = body.Close()
				return err
			}
			if progressed && firstTimerC != nil {
				if !firstTimer.Stop() {
					select {
					case <-firstTimer.C:
					default:
					}
				}
				firstTimerC = nil
			}
		}
	}
}
