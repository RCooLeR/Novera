package bigfile

import (
	"context"
	"errors"
)

var ErrServiceStopped = errors.New("big-file service is shutting down")

func (s *FileService) ensureServiceRunning() error {
	if s == nil || s.reg == nil {
		return ErrServiceStopped
	}
	s.serviceMu.Lock()
	stopping := s.serviceStopping
	s.serviceMu.Unlock()
	if stopping {
		return ErrServiceStopped
	}
	return nil
}

func (s *FileService) beginServiceShutdown() (bool, chan struct{}) {
	if s == nil {
		done := make(chan struct{})
		close(done)
		return false, done
	}
	s.serviceMu.Lock()
	if s.serviceStopDone == nil {
		s.serviceStopDone = make(chan struct{})
	}
	done := s.serviceStopDone
	if s.serviceStopping {
		s.serviceMu.Unlock()
		return false, done
	}
	s.serviceStopping = true
	s.serviceMu.Unlock()
	return true, done
}

// ServiceShutdown permanently closes service admission, cancels interactive
// searches and the active transform job, cancels all indexers, then drains
// every retained document lease. Concurrent calls are idempotent and observe
// the same terminal error.
func (s *FileService) ServiceShutdown() error {
	owner, done := s.beginServiceShutdown()
	if !owner {
		<-done
		if s == nil {
			return nil
		}
		s.serviceMu.Lock()
		err := s.serviceStopErr
		s.serviceMu.Unlock()
		return err
	}

	s.cancelAllSearchRequests()
	jobDone := s.jobs().stop()
	registryDone := s.reg.BeginShutdown()
	<-jobDone
	<-registryDone
	registryErr := s.reg.Shutdown(context.Background())

	s.sqlMu.Lock()
	s.sqlSummary = make(map[string]cachedSQLSummary)
	s.sqlMu.Unlock()
	s.editMu.Lock()
	s.preparedEdits = make(map[string]struct{})
	s.editMu.Unlock()

	s.serviceMu.Lock()
	s.serviceStopErr = registryErr
	close(done)
	s.serviceMu.Unlock()
	return registryErr
}
