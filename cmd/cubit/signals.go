package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// Exit codes for a stop by signal: 128 plus the signal number, the shell
// convention. They sit in the "3 and up: no usable result" half of the contract.
const (
	exitSIGINT  = 128 + int(syscall.SIGINT)
	exitSIGTERM = 128 + int(syscall.SIGTERM)
)

// exitForSignal maps a stop signal to its exit code.
func exitForSignal(sig os.Signal) int {
	if sig == syscall.SIGTERM {
		return exitSIGTERM
	}
	return exitSIGINT
}

// watchSignals returns a context that is canceled when SIGINT or SIGTERM
// arrives, a function that reports which one it was (nil if none), and a stop
// function that releases the handler.
func watchSignals(parent context.Context) (context.Context, func() os.Signal, func()) {
	ctx, cancel := context.WithCancel(parent)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	var mu sync.Mutex
	var got os.Signal
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case s := <-ch:
			mu.Lock()
			got = s
			mu.Unlock()
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, func() os.Signal {
			mu.Lock()
			defer mu.Unlock()
			return got
		}, func() {
			signal.Stop(ch)
			cancel()
			<-done
		}
}
