package cloud

import (
	"errors"
	"sync"
	"time"

	"github.com/dropfile/HankServerside/internal/protocol"
)

var (
	ErrTransferNotFound      = errors.New("transfer not found")
	ErrTransferBusy          = errors.New("transfer is already active")
	ErrTransferOffsetInvalid = errors.New("transfer offset is invalid")
)

type transferRegistry struct {
	mu        sync.Mutex
	transfers map[string]*transferSession
	attempts  map[string]*transferAttempt
}

type transferSession struct {
	ID        string
	HomeID    string
	AgentID   string
	JobID     string
	Operation string
	SourceID  string
	Path      string
	TokenHash string
	CreatedAt time.Time
	ExpiresAt time.Time

	mu          sync.Mutex
	size        int64
	nextOffset  int64
	lastError   *protocol.ErrorPayload
	active      bool
	cancelled   bool
	attemptID   string
	resumeCount int
	completedAt *time.Time
}

type transferAttempt struct {
	wireMu                              sync.Mutex
	negotiated                          bool
	received, acknowledged, expectedEnd int64
	failure                             *protocol.ErrorPayload
	Length                              int64
	done                                chan struct{}
	endOnce                             sync.Once
	readyOnce                           sync.Once
	completeOnce                        sync.Once
	target                              agentReplyBinding
	Session                             *transferSession
	ID                                  string
	Offset                              int64
	ReadyCh                             chan transferReadyResult
	DataCh                              chan transferDataFrame
	CompleteCh                          chan transferCompleteResult
}

type transferReadyResult struct {
	Ready protocol.FileTransferReady
	Error *protocol.ErrorPayload
}

type transferDataFrame struct {
	Offset int64
	Data   []byte
	Error  *protocol.ErrorPayload
}

type transferCompleteResult struct {
	Complete protocol.FileTransferComplete
	Error    *protocol.ErrorPayload
}

func newTransferRegistry() *transferRegistry {
	return &transferRegistry{
		transfers: make(map[string]*transferSession),
		attempts:  make(map[string]*transferAttempt),
	}
}

func (r *transferRegistry) Create(homeID string, agentID string, jobID string, operation string, sourceID string, path string, ttl time.Duration) (*transferSession, string) {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}

	id := newID("xfer")
	rawToken := newToken()
	session := &transferSession{
		ID:        id,
		HomeID:    homeID,
		AgentID:   agentID,
		JobID:     jobID,
		Operation: operation,
		SourceID:  sourceID,
		Path:      path,
		TokenHash: hashToken(rawToken),
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(ttl),
	}

	r.mu.Lock()
	r.transfers[id] = session
	r.mu.Unlock()

	return session, rawToken
}

func (r *transferRegistry) Get(id string) (*transferSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	session, ok := r.transfers[id]
	if !ok {
		return nil, false
	}
	if time.Now().UTC().After(session.ExpiresAt) {
		delete(r.transfers, id)
		return nil, false
	}
	return session, true
}

func (r *transferRegistry) Restore(session *transferSession) {
	if session == nil || session.ID == "" {
		return
	}
	r.mu.Lock()
	r.transfers[session.ID] = session
	r.mu.Unlock()
}

func (r *transferRegistry) Delete(id string) {
	r.mu.Lock()
	delete(r.transfers, id)
	r.mu.Unlock()
}

func (r *transferRegistry) Authorize(id string, rawToken string, operation string) (*transferSession, error) {
	session, ok := r.Get(id)
	if !ok {
		return nil, ErrTransferNotFound
	}
	if session.Operation != operation || session.TokenHash != hashToken(rawToken) {
		return nil, ErrTransferNotFound
	}
	return session, nil
}

func (r *transferRegistry) BeginAttempt(session *transferSession, offset int64, target agentReplyBinding) (*transferAttempt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	attempt, err := session.BeginAttempt(offset)
	if err != nil {
		return nil, err
	}
	attempt.Session = session
	attempt.target = target

	r.attempts[attempt.ID] = attempt

	return attempt, nil
}

func (r *transferRegistry) GetAttempt(sender agentReplyBinding, envelope protocol.Envelope) (*transferAttempt, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	attempt, ok := r.attempts[envelope.RequestID]
	if !ok || !attempt.target.accepts(sender, envelope) {
		return nil, false
	}
	return attempt, true
}

func (r *transferRegistry) EndAttempt(id string) {
	r.mu.Lock()
	attempt, ok := r.attempts[id]
	if ok {
		delete(r.attempts, id)
	}
	r.mu.Unlock()
	if ok {
		attempt.endOnce.Do(func() { close(attempt.done) })
		attempt.Session.EndAttempt()
	}
}

func (s *transferSession) Snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()

	payload := map[string]any{
		"transfer_id":  s.ID,
		"job_id":       s.JobID,
		"source_id":    s.SourceID,
		"path":         s.Path,
		"operation":    s.Operation,
		"size":         s.size,
		"next_offset":  s.nextOffset,
		"resume_count": s.resumeCount,
		"created_at":   s.CreatedAt,
		"expires_at":   s.ExpiresAt,
		"active":       s.active,
		"completed_at": s.completedAt,
		"resumable":    true,
	}
	if s.lastError != nil {
		payload["last_error"] = s.lastError
	}
	return payload
}

func (s *transferSession) BeginAttempt(offset int64) (*transferAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancelled || time.Now().UTC().After(s.ExpiresAt) {
		return nil, ErrTransferNotFound
	}
	if s.active {
		return nil, ErrTransferBusy
	}
	if offset < 0 {
		return nil, ErrTransferOffsetInvalid
	}

	if s.Operation == protocol.FileTransferOperationUpload {
		if offset > s.nextOffset {
			return nil, ErrTransferOffsetInvalid
		}
	}
	if s.Operation == protocol.FileTransferOperationDownload && s.size > 0 && offset > s.size {
		return nil, ErrTransferOffsetInvalid
	}

	s.active = true
	s.attemptID = newID("xfertry")
	s.resumeCount++
	s.lastError = nil
	s.completedAt = nil

	return &transferAttempt{
		ID:           s.attemptID,
		done:         make(chan struct{}),
		Offset:       offset,
		received:     offset,
		acknowledged: offset,
		ReadyCh:      make(chan transferReadyResult, 1),
		DataCh:       make(chan transferDataFrame, 16),
		CompleteCh:   make(chan transferCompleteResult, 1),
	}, nil
}

func (s *transferSession) EndAttempt() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = false
	s.attemptID = ""
}

func (s *transferSession) IsAttempt(attemptID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active && s.attemptID == attemptID
}

func (s *transferSession) MarkReady(ready protocol.FileTransferReady) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.size = ready.Size
	if s.Operation == protocol.FileTransferOperationUpload && ready.Offset > s.nextOffset {
		s.nextOffset = ready.Offset
	}
}

func (s *transferSession) Advance(nextOffset int64, totalSize int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if nextOffset > s.nextOffset {
		s.nextOffset = nextOffset
	}
	if totalSize > s.size {
		s.size = totalSize
	}
}

func (s *transferSession) Fail(err *protocol.ErrorPayload) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = err
}

func (s *transferSession) Complete(done protocol.FileTransferComplete) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.size = done.Size
	if done.Offset > s.nextOffset {
		s.nextOffset = done.Offset
	}
	now := time.Now().UTC()
	s.completedAt = &now
	s.lastError = nil
}

func (s *transferSession) NextOffset() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextOffset
}

func (s *transferSession) Progress() (int64, int64, *time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.size, s.nextOffset, s.completedAt
}

func (s *transferSession) LastError() *protocol.ErrorPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastError
}

// Singleton frames must never block the shared reader on duplicates.
func (a *transferAttempt) deliverReady(result transferReadyResult) {
	a.readyOnce.Do(func() {
		select {
		case <-a.done:
		case a.ReadyCh <- result:
		}
	})
}
func (a *transferAttempt) deliverComplete(result transferCompleteResult) {
	a.completeOnce.Do(func() {
		select {
		case <-a.done:
		case a.CompleteCh <- result:
		}
	})
}

// abort never waits for an HTTP consumer or writes on the shared agent socket.
func (a *transferAttempt) abort(failure *protocol.ErrorPayload) {
	a.wireMu.Lock()
	if a.failure == nil {
		a.failure = failure
	}
	a.wireMu.Unlock()
	a.endOnce.Do(func() { close(a.done) })
}
func (a *transferAttempt) failureResult() *protocol.ErrorPayload {
	a.wireMu.Lock()
	defer a.wireMu.Unlock()
	if a.failure != nil {
		return a.failure
	}
	return &protocol.ErrorPayload{Code: "transfer_cancelled", Message: "transfer ended"}
}
func (a *transferAttempt) deliverData(frame transferDataFrame) {
	select {
	case <-a.done:
		return
	default:
	}
	failure := frame.Error
	a.wireMu.Lock()
	if failure == nil && (a.Session.Operation != protocol.FileTransferOperationDownload || !a.negotiated ||
		frame.Offset != a.received || len(frame.Data) == 0 || len(frame.Data) > protocol.FileTransferMaxChunkBytes ||
		int64(len(frame.Data)) > a.expectedEnd-a.received ||
		int64(len(frame.Data)) > protocol.FileTransferWindowBytes-(a.received-a.acknowledged)) {
		failure = &protocol.ErrorPayload{Code: "invalid_transfer_chunk", Message: "download exceeded its negotiated window or stream bounds"}
	}
	if failure == nil {
		a.received += int64(len(frame.Data))
	}
	a.wireMu.Unlock()
	if failure != nil {
		a.abort(failure)
		return
	}
	select {
	case <-a.done:
	case a.DataCh <- frame:
	default:
		a.abort(&protocol.ErrorPayload{Code: "transfer_window_exceeded", Message: "download queue exceeded its negotiated window"})
	}
}

func (r *transferRegistry) CancelJob(homeID, jobID string) []*transferAttempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, session := range r.transfers {
		if session.HomeID == homeID && session.JobID == jobID {
			session.mu.Lock()
			session.cancelled = true
			session.mu.Unlock()
		}
	}
	var attempts []*transferAttempt
	for _, a := range r.attempts {
		if a.Session.HomeID == homeID && a.Session.JobID == jobID {
			a.abort(&protocol.ErrorPayload{Code: "transfer_cancelled", Message: "cancelled by user"})
			attempts = append(attempts, a)
		}
	}
	return attempts
}
