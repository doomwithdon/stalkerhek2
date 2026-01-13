package webui

import (
	"context"
	"sync"
)

type runner struct {
	cancel context.CancelFunc
	done   chan struct{}
}

var (
	runMu   sync.RWMutex
	runners = map[int]*runner{}
)

// RegisterRunner registers a cancel func for a profile ID
func RegisterRunner(id int, cancel context.CancelFunc) {
	runMu.Lock()
	prev := runners[id]
	runners[id] = &runner{cancel: cancel}
	runMu.Unlock()
	if prev != nil {
		prev.cancel()
	}
}

func RegisterRunnerDone(id int, done chan struct{}) {
	runMu.Lock()
	r := runners[id]
	if r != nil {
		r.done = done
	}
	runMu.Unlock()
}

// StopRunner stops a running profile by ID
func StopRunner(id int) error {
	runMu.Lock()
	r := runners[id]
	if r == nil {
		runMu.Unlock()
		return nil
	}
	delete(runners, id)
	runMu.Unlock()
	r.cancel()
	return nil
}

func StopRunnerWithDone(id int) (done chan struct{}, ok bool) {
	runMu.Lock()
	r := runners[id]
	if r == nil {
		runMu.Unlock()
		return nil, false
	}
	done = r.done
	delete(runners, id)
	runMu.Unlock()
	r.cancel()
	return done, true
}

// IsRunning checks if profile is registered
func IsRunning(id int) bool {
	runMu.RLock()
	defer runMu.RUnlock()
	_, ok := runners[id]
	return ok
}
