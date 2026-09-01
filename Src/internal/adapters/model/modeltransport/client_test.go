package modeltransport

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDoClassifiesResponseHeaderTimeoutWithoutLeakingURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(80 * time.Millisecond)
	}))
	defer server.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 20 * time.Millisecond
	client := &http.Client{Transport: transport}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/secret-path", nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = Do(client, request)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("error=%T %v", err, err)
	}
	if failure.Kind != KindResponseTimeout || failure.Phase != PhaseResponseHeaders || !failure.TimedOut {
		t.Fatalf("failure=%+v", failure)
	}
	if strings.Contains(err.Error(), "secret-path") || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("unsafe error=%q", err.Error())
	}
}

func TestScanLinesClassifiesStreamIdleTimeout(t *testing.T) {
	reader, writer := net.Pipe()
	defer writer.Close()

	err := ScanLines(context.Background(), reader, io.Reader(reader), 1024, 20*time.Millisecond, func(string) error { return nil })
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("error=%T %v", err, err)
	}
	if failure.Kind != KindStreamIdleTimeout || failure.Phase != PhaseResponseBody || !failure.TimedOut {
		t.Fatalf("failure=%+v", failure)
	}
}

func TestScanLinesKeepAliveDoesNotSatisfyFirstTokenDeadline(t *testing.T) {
	reader, writer := net.Pipe()
	defer reader.Close()
	defer writer.Close()
	go func() {
		for index := 0; index < 8; index++ {
			_, _ = io.WriteString(writer, ": keep-alive\n")
			time.Sleep(5 * time.Millisecond)
		}
	}()

	err := ScanLinesWithProgress(context.Background(), reader, io.Reader(reader), 1024, 20*time.Millisecond, 50*time.Millisecond, func(string) (bool, error) {
		return false, nil
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindFirstTokenTimeout || failure.Phase != PhaseResponseBody || !failure.TimedOut {
		t.Fatalf("failure=%+v err=%v", failure, err)
	}
}
