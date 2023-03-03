// Package xmain provides a standard stub for the main of a command handling logging,
// flags, signals and shutdown.
package xmain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"syscall"
	"time"

	"oss.terrastruct.com/util-go/cmdlog"
	"oss.terrastruct.com/util-go/xos"
)

type RunFunc func(context.Context, *State) error

func Main(run RunFunc) {
	name := ""
	args := []string(nil)
	if len(os.Args) > 0 {
		name = os.Args[0]
		args = os.Args[1:]
	}

	ms := &State{
		Name: name,

		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,

		Env: xos.NewEnv(os.Environ()),

		FS: os.DirFS("/"),
	}
	ms.Log = cmdlog.New(ms.Env, os.Stderr)
	ms.Opts = NewOpts(ms.Env, ms.Log, args)

	wd, err := os.Getwd()
	if err != nil {
		ms.mainFatal(err)
	}
	ms.Dir = wd

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)

	err = ms.Main(context.Background(), sigs, run)
	if err != nil {
		ms.mainFatal(err)
	}
}

func (ms *State) mainFatal(err error) {
	code := 1
	msg := ""
	usage := false

	var eerr ExitError
	var uerr UsageError
	if errors.As(err, &eerr) {
		code = eerr.Code
		msg = eerr.Message
	} else if errors.As(err, &uerr) {
		msg = err.Error()
		usage = true
	} else {
		msg = err.Error()
	}

	if msg != "" {
		ms.Log.Error.Print(msg)
		if usage {
			ms.Log.Error.Print("Run with --help to see usage.")
		}
	}
	os.Exit(code)
}

type State struct {
	Name string

	Stdin  io.Reader
	Stdout io.WriteCloser
	Stderr io.WriteCloser

	Log  *cmdlog.Logger
	Env  *xos.Env
	Opts *Opts

	Dir string
	FS  fs.FS
}

func (ms *State) Main(ctx context.Context, sigs <-chan os.Signal, run func(context.Context, *State) error) error {
	defer ms.Stdout.Close()
	defer ms.Stderr.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		defer close(done)
		done <- run(ctx, ms)
	}()

	select {
	case err := <-done:
		return err
	case sig := <-sigs:
		ms.Log.Warn.Printf("received signal %v: shutting down...", sig)
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("failed to shutdown: %w", err)
			}
			if sig == syscall.SIGTERM {
				// We successfully shutdown.
				return nil
			}
			return ExitError{Code: 1}
		case <-time.After(time.Minute):
			return ExitError{
				Code:    1,
				Message: "took longer than 1 minute to shutdown: exiting forcefully",
			}
		}
	}
}

type ExitError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func ExitErrorf(code int, msg string, v ...interface{}) ExitError {
	return ExitError{
		Code:    code,
		Message: fmt.Sprintf(msg, v...),
	}
}

func (ee ExitError) Error() string {
	s := fmt.Sprintf("exiting with code %d", ee.Code)
	if ee.Message != "" {
		s += ": " + ee.Message
	}
	return s
}

type UsageError struct {
	Message string `json:"message"`
}

func UsageErrorf(msg string, v ...interface{}) UsageError {
	return UsageError{
		Message: fmt.Sprintf(msg, v...),
	}
}

func (ue UsageError) Error() string {
	return fmt.Sprintf("bad usage: %s", ue.Message)
}

func (ms *State) ReadPath(fp string) ([]byte, error) {
	if fp == "-" {
		return io.ReadAll(ms.Stdin)
	}
	return os.ReadFile(fp)
}

func (ms *State) WritePath(fp string, p []byte) error {
	if fp == "-" {
		_, err := ms.Stdout.Write(p)
		if err != nil {
			return err
		}
		return ms.Stdout.Close()
	}
	return os.WriteFile(fp, p, 0644)
}
