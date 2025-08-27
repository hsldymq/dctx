package dctx

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrAlreadyDone = errors.New("dynamic context is already done, cannot switch underlying context")

// DynamicContext is a Context implementation that can dynamically switch underlying contexts
type DynamicContext struct {
	underlying  context.Context             // underlying context
	done        chan struct{}               // fixed done channel
	err         error                       // error information
	values      map[interface{}]interface{} // custom key-value storage
	cancelWatch context.CancelFunc          // used to cancel watching goroutine

	mu *sync.RWMutex
}

// NewDynamicContext creates a new dynamic Context with its cancel function
func NewDynamicContext(underlying context.Context) (*DynamicContext, context.CancelFunc) {
	if underlying == nil {
		underlying = context.Background()
	}

	dc := &DynamicContext{
		underlying: underlying,
		done:       make(chan struct{}),
		values:     make(map[interface{}]interface{}),

		mu: &sync.RWMutex{},
	}

	// start watching goroutine
	dc.startWatching()

	return dc, dc.close
}

// startWatching starts a goroutine to watch the current underlying context
func (dc *DynamicContext) startWatching() {
	// cancel previous watching
	if dc.cancelWatch != nil {
		dc.cancelWatch()
	}

	// if already done, no need to watch
	select {
	case <-dc.done:
		return
	default:
	}

	// get current underlying context
	underlying := dc.underlying

	// if underlying context is already done, handle immediately
	select {
	case <-underlying.Done():
		// check done status again to avoid duplicate close
		select {
		case <-dc.done:
			// already done, no need to handle
		default:
			dc.err = underlying.Err()
			close(dc.done)
		}
		return
	default:
	}

	// start new watching goroutine
	watchCtx, cancel := context.WithCancel(context.Background())
	dc.cancelWatch = cancel

	go func() {
		defer cancel()

		select {
		case <-underlying.Done():
			dc.mu.Lock()
			// check again if already done (status might have changed while acquiring lock)
			select {
			case <-dc.done:
				// already done, no need to handle
			default:
				dc.err = underlying.Err()
				close(dc.done)
			}
			dc.mu.Unlock()
		case <-watchCtx.Done():
			// watching cancelled (usually because context switched or DynamicContext closed)
			return
		}
	}()
}

// SwitchContext switches the underlying context
func (dc *DynamicContext) SwitchContext(newCtx context.Context) error {
	if newCtx == nil {
		newCtx = context.Background()
	}

	dc.mu.Lock()
	defer dc.mu.Unlock()

	// if already done, cannot switch anymore
	if dc.isDone() {
		return ErrAlreadyDone
	}

	// update underlying context
	dc.underlying = newCtx

	// restart watching new underlying context
	dc.startWatching()

	return nil
}

// Done returns the fixed done channel
func (dc *DynamicContext) Done() <-chan struct{} {
	return dc.done
}

// Deadline returns the deadline of the underlying context
func (dc *DynamicContext) Deadline() (deadline time.Time, ok bool) {
	dc.mu.RLock()
	defer dc.mu.RUnlock()

	return dc.underlying.Deadline()
}

// Err returns error information
func (dc *DynamicContext) Err() error {
	dc.mu.RLock()
	defer dc.mu.RUnlock()

	return dc.underlying.Err()
}

// Value implements hierarchical value lookup
func (dc *DynamicContext) Value(key interface{}) interface{} {
	dc.mu.RLock()
	defer dc.mu.RUnlock()

	return dc.underlying.Value(key)
}

// SetValue sets key-value for the dynamic context itself
func (dc *DynamicContext) SetValue(key, value interface{}) {
	dc.mu.Lock()
	defer dc.mu.Unlock()

	dc.values[key] = value
}

// DeleteValue deletes key-value from the dynamic context itself
func (dc *DynamicContext) DeleteValue(key interface{}) {
	dc.mu.Lock()
	defer dc.mu.Unlock()

	delete(dc.values, key)
}

// isDone checks if already done
func (dc *DynamicContext) isDone() bool {
	select {
	case <-dc.done:
		return true
	default:
		return false
	}
}

// close closes the dynamic context and stops watching
func (dc *DynamicContext) close() {
	dc.mu.Lock()
	defer dc.mu.Unlock()

	if dc.cancelWatch != nil {
		dc.cancelWatch()
		dc.cancelWatch = nil
	}

	select {
	case <-dc.done:
		// already done, no need to handle
	default:
		dc.err = context.Canceled
		close(dc.done)
	}
}
