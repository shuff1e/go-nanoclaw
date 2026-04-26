package runtime

import (
	"context"
	"fmt"
	"sync"
)

type Dispatcher struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		locks: make(map[string]*sync.Mutex),
	}
}

func (d *Dispatcher) Run(ctx context.Context, execCtx *ExecutionContext, fn func(context.Context) error) error {
	sessionLock, agentLock := d.locksFor(execCtx)
	sessionLock.Lock()
	defer sessionLock.Unlock()

	if agentLock != sessionLock {
		agentLock.Lock()
		defer agentLock.Unlock()
	}

	return fn(ctx)
}

func (d *Dispatcher) locksFor(execCtx *ExecutionContext) (*sync.Mutex, *sync.Mutex) {
	sessionKey := d.sessionKey(execCtx)
	agentKey := d.agentKey(execCtx)

	d.mu.Lock()
	defer d.mu.Unlock()

	sessionLock := d.lockForKey(sessionKey)
	agentLock := d.lockForKey(agentKey)
	return sessionLock, agentLock
}

func (d *Dispatcher) lockForKey(key string) *sync.Mutex {
	lock, ok := d.locks[key]
	if ok {
		return lock
	}
	lock = &sync.Mutex{}
	d.locks[key] = lock
	return lock
}

func (d *Dispatcher) sessionKey(execCtx *ExecutionContext) string {
	if execCtx == nil {
		return "session:global"
	}
	if execCtx.IDs.SessionID != "" {
		return "session:" + execCtx.IDs.SessionID
	}
	if execCtx.AgentID != "" {
		return "session:agent:" + execCtx.AgentID
	}
	return "session:global"
}

func (d *Dispatcher) agentKey(execCtx *ExecutionContext) string {
	if execCtx == nil || execCtx.AgentID == "" {
		return "agent:global"
	}
	return fmt.Sprintf("agent:%s", execCtx.AgentID)
}
