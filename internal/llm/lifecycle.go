package llm

import (
	"context"
	"fmt"
)

// beginRequest is the lifecycle linearization point. A request admitted before
// shutdown is installed in requests and therefore canceled/drained by shutdown;
// a request arriving after the permanent gate is raised is rejected. Stream
// leases cover synchronous settings/credential setup as well as the HTTP
// goroutine, so setup races cannot escape a shutdown snapshot.
func (s *Service) beginRequest(kind requestKind) (*requestLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return nil, ErrServiceShuttingDown
	}
	switch kind {
	case requestKindStream:
		limit := s.streamLimit
		if limit <= 0 {
			limit = maxConcurrentStreams
		}
		if s.streams >= limit {
			return nil, fmt.Errorf("%w: maximum %d", ErrTooManyStreams, limit)
		}
	case requestKindModelList:
		limit := s.modelListLimit
		if limit <= 0 {
			limit = maxConcurrentModelLists
		}
		if s.modelLists >= limit {
			return nil, fmt.Errorf("%w: maximum %d", ErrTooManyModelLists, limit)
		}
	default:
		panic("llm: unknown request kind")
	}

	var id string
	switch kind {
	case requestKindStream:
		s.seq++
		id = fmt.Sprintf("req-%d", s.seq)
		s.streams++
	case requestKindModelList:
		s.modelSeq++
		id = fmt.Sprintf("models-%d", s.modelSeq)
		s.modelLists++
	}
	ctx, cancel := context.WithCancel(context.Background())
	lease := &requestLease{id: id, kind: kind, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	if s.requests == nil {
		s.requests = make(map[string]*requestLease)
	}
	s.requests[id] = lease
	return lease, nil
}

func requestSetupError(lease *requestLease) error {
	if lease == nil || lease.ctx.Err() != nil {
		return ErrServiceShuttingDown
	}
	return nil
}

// finishRequest releases a lease only after all synchronous setup or streaming
// cleanup has returned. Cancel deliberately does not remove leases early:
// shutdown and the admission limit must continue to see a canceled-but-still-
// unwinding RoundTripper.
func (s *Service) finishRequest(lease *requestLease) {
	if lease == nil {
		return
	}
	lease.cancel()
	s.mu.Lock()
	if current := s.requests[lease.id]; current == lease {
		delete(s.requests, lease.id)
		if lease.kind == requestKindStream {
			s.streams--
			if s.streams < 0 {
				s.mu.Unlock()
				panic("llm: negative stream lease count")
			}
		} else if lease.kind == requestKindModelList {
			s.modelLists--
			if s.modelLists < 0 {
				s.mu.Unlock()
				panic("llm: negative model-list lease count")
			}
		}
		close(lease.done)
	}
	s.mu.Unlock()
}

// ServiceShutdown permanently closes request admission, cancels every pending
// setup and in-flight HTTP request, and waits a bounded interval for their
// cleanup. Standard net/http transports honor request cancellation; the bound
// prevents a custom or broken RoundTripper from hanging application exit.
func (s *Service) ServiceShutdown() error {
	if s == nil {
		return nil
	}
	s.shutdownOnce.Do(func() {
		s.shutdownErr = s.shutdown()
	})
	return s.shutdownErr
}

func (s *Service) shutdown() error {
	s.mu.Lock()
	s.stopping = true
	requests := make([]*requestLease, 0, len(s.requests))
	for _, lease := range s.requests {
		requests = append(requests, lease)
	}
	timeout := s.shutdownTimeout
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}
	s.mu.Unlock()

	for _, lease := range requests {
		lease.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for _, lease := range requests {
		select {
		case <-lease.done:
			continue
		case <-ctx.Done():
			// Do not report a timeout if cleanup closed done concurrently with
			// the timer. A non-blocking recheck resolves that boundary race.
			select {
			case <-lease.done:
				continue
			default:
			}
			return fmt.Errorf("%w after %s waiting for request %q", ErrShutdownTimeout, timeout, lease.id)
		}
	}
	return nil
}
