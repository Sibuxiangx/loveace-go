// Package mem is the in-memory store.Store used by unit tests (and by
// loveaced when started without LOVEACED_DATABASE_URL in dev mode). Data
// lives only for the process lifetime.
package mem

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Sibuxiangx/loveace-go/server/internal/store"
)

// Store is a mutex-guarded in-memory store.
type Store struct {
	mu            sync.Mutex
	users         map[string]*store.User // by id
	byAccount     map[string]string      // accountHash -> user id
	creds         map[string]*store.Credential
	devices       map[string]*store.Device
	devByUser     map[string][]string // userID -> device ids
	tokens        map[string]*store.RefreshToken
	audit         []store.AuditEvent
	poolSlots     map[string]*store.PoolSlot
	userSnaps     map[string]*store.UserSnapshot
	announcements map[string]*store.Announcement
	acks          map[string]time.Time // "annID/userID" -> at
	calTerms      map[string]*store.CalendarTerm
	calRules      map[string]*store.CalendarRule
	idem          map[string]*store.IdempotencyRecord // "userID\x00key"
	evalTasks     map[string]*store.TeacherEvalTask
	evalCourses   map[string][]store.TeacherEvalTaskCourse
	evalLogs      map[string][]store.TeacherEvalLog
	evalLogSeq    int64
}

// New creates an empty store.
func New() *Store {
	return &Store{
		users:         map[string]*store.User{},
		byAccount:     map[string]string{},
		creds:         map[string]*store.Credential{},
		devices:       map[string]*store.Device{},
		devByUser:     map[string][]string{},
		tokens:        map[string]*store.RefreshToken{},
		poolSlots:     map[string]*store.PoolSlot{},
		userSnaps:     map[string]*store.UserSnapshot{},
		announcements: map[string]*store.Announcement{},
		acks:          map[string]time.Time{},
		calTerms:      map[string]*store.CalendarTerm{},
		calRules:      map[string]*store.CalendarRule{},
		idem:          map[string]*store.IdempotencyRecord{},
		evalTasks:     map[string]*store.TeacherEvalTask{},
		evalCourses:   map[string][]store.TeacherEvalTaskCourse{},
		evalLogs:      map[string][]store.TeacherEvalLog{},
	}
}

func (s *Store) GetOrCreateUser(_ context.Context, accountHash string) (*store.User, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.byAccount[accountHash]; ok {
		return s.users[id], false, nil
	}
	u := &store.User{
		ID:              store.NewID(),
		AccountHash:     accountHash,
		CredentialState: store.CredUnverified,
		CreatedAt:       time.Now(),
	}
	s.users[u.ID] = u
	s.byAccount[accountHash] = u.ID
	return u, true, nil
}

func (s *Store) GetUser(_ context.Context, userID string) (*store.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[userID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return u, nil
}

func (s *Store) UpsertCredential(_ context.Context, cred store.Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := cred
	s.creds[cred.UserID] = &cp
	return nil
}

func (s *Store) GetCredential(_ context.Context, userID string) (*store.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.creds[userID]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (s *Store) SetCredentialState(_ context.Context, userID, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[userID]
	if !ok {
		return store.ErrNotFound
	}
	u.CredentialState = state
	return nil
}

// ListCredentialUserIDs returns up to limit users holding stored
// credentials (mem: insertion order is not tracked; any order serves).
func (s *Store) ListCredentialUserIDs(_ context.Context, limit int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, limit)
	for id := range s.creds {
		if len(out) >= limit {
			break
		}
		out = append(out, id)
	}
	return out, nil
}

func (s *Store) CreateDevice(_ context.Context, userID, name, platform string) (*store.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := &store.Device{
		ID:        store.NewID(),
		UserID:    userID,
		Name:      name,
		Platform:  platform,
		CreatedAt: time.Now(),
	}
	s.devices[d.ID] = d
	s.devByUser[userID] = append(s.devByUser[userID], d.ID)
	return d, nil
}

func (s *Store) ListDevices(_ context.Context, userID string) ([]store.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.Device
	for _, id := range s.devByUser[userID] {
		if d := s.devices[id]; d != nil && d.RevokedAt == nil {
			out = append(out, *d)
		}
	}
	return out, nil
}

func (s *Store) GetDevice(_ context.Context, userID, deviceID string) (*store.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[deviceID]
	if !ok || d.UserID != userID {
		return nil, store.ErrNotFound
	}
	return d, nil
}

func (s *Store) RevokeDevice(_ context.Context, userID, deviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[deviceID]
	if !ok || d.UserID != userID {
		return store.ErrNotFound
	}
	now := time.Now()
	d.RevokedAt = &now
	return nil
}

func (s *Store) InsertRefreshToken(_ context.Context, t store.RefreshToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := t
	s.tokens[t.Hash] = &cp
	return nil
}

func (s *Store) GetRefreshToken(_ context.Context, hash string) (*store.RefreshToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[hash]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *t
	return &cp, nil
}

func (s *Store) RotateRefreshToken(_ context.Context, oldHash, newHash string, newExpiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[oldHash]
	if !ok {
		return store.ErrNotFound
	}
	t.RotatedTo = newHash
	nt := *s.tokens[oldHash]
	nt.Hash = newHash
	nt.ExpiresAt = newExpiresAt
	nt.RotatedTo = ""
	nt.CreatedAt = time.Now()
	s.tokens[newHash] = &nt
	return nil
}

func (s *Store) RevokeDeviceTokens(_ context.Context, deviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tokens {
		if t.DeviceID == deviceID {
			t.Revoked = true
		}
	}
	return nil
}

// PruneRotatedRefreshTokens deletes superseded (rotated) refresh-token
// rows created before the cutoff.
func (s *Store) PruneRotatedRefreshTokens(_ context.Context, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for h, t := range s.tokens {
		if t.RotatedTo != "" && t.CreatedAt.Before(before) {
			delete(s.tokens, h)
		}
	}
	return nil
}

func (s *Store) AppendAudit(_ context.Context, e store.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, e)
	return nil
}

func (s *Store) PutPoolSlot(_ context.Context, slot store.PoolSlot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := slot
	s.poolSlots[slot.ID] = &cp
	return nil
}

func (s *Store) DeletePoolSlot(_ context.Context, slotID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.poolSlots, slotID)
	return nil
}

func (s *Store) ListPoolSlots(_ context.Context) ([]store.PoolSlot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.PoolSlot, 0, len(s.poolSlots))
	for _, v := range s.poolSlots {
		out = append(out, *v)
	}
	return out, nil
}

func (s *Store) PutUserSnapshot(_ context.Context, snap store.UserSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := snap
	s.userSnaps[snap.UserID] = &cp
	return nil
}

func (s *Store) GetUserSnapshot(_ context.Context, userID string) (*store.UserSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.userSnaps[userID]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (s *Store) DeleteUserSnapshot(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.userSnaps, userID)
	return nil
}

func (s *Store) CreateAnnouncement(_ context.Context, a store.Announcement) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := a
	s.announcements[a.ID] = &cp
	return nil
}

func (s *Store) ListAnnouncements(_ context.Context, activeOnly bool) ([]store.Announcement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.Announcement, 0, len(s.announcements))
	for _, a := range s.announcements {
		if activeOnly && !a.Active {
			continue
		}
		out = append(out, *a)
	}
	return out, nil
}

func (s *Store) DeactivateAnnouncement(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.announcements[id]
	if !ok {
		return store.ErrNotFound
	}
	a.Active = false
	return nil
}

func (s *Store) AckAnnouncement(_ context.Context, userID, announcementID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := announcementID + "/" + userID
	if _, ok := s.acks[key]; !ok {
		s.acks[key] = time.Now()
	}
	return nil
}

func (s *Store) ListAcks(_ context.Context, userID string) (map[string]time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]time.Time{}
	for key, at := range s.acks {
		annID, uid, _ := strings.Cut(key, "/")
		if uid == userID {
			out[annID] = at
		}
	}
	return out, nil
}

func (s *Store) UpsertCalendarTerm(_ context.Context, t store.CalendarTerm) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := t
	s.calTerms[t.ID] = &cp
	return nil
}

func (s *Store) ListCalendarTerms(_ context.Context) ([]store.CalendarTerm, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.CalendarTerm, 0, len(s.calTerms))
	for _, t := range s.calTerms {
		out = append(out, *t)
	}
	return out, nil
}

func (s *Store) AddCalendarRule(_ context.Context, r store.CalendarRule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := r
	s.calRules[r.ID] = &cp
	return nil
}

func (s *Store) ListCalendarRules(_ context.Context, termID, status string) ([]store.CalendarRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.CalendarRule
	for _, r := range s.calRules {
		if r.TermID != termID {
			continue
		}
		if status != "" && r.Status != status {
			continue
		}
		out = append(out, *r)
	}
	return out, nil
}

func (s *Store) DeleteCalendarRule(_ context.Context, ruleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.calRules[ruleID]
	if !ok {
		return store.ErrNotFound
	}
	if r.Status != "draft" {
		return store.ErrNotFound // published rules retire via a new version
	}
	delete(s.calRules, ruleID)
	return nil
}

func (s *Store) PublishCalendar(_ context.Context, termID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	term, ok := s.calTerms[termID]
	if !ok {
		return 0, store.ErrNotFound
	}
	term.Version++
	for _, r := range s.calRules {
		if r.TermID != termID {
			continue
		}
		if r.Status == "draft" {
			r.Status = "published"
			r.Version = term.Version
		} else if r.Status == "published" && r.Version < term.Version {
			r.Status = "retired"
		}
	}
	return term.Version, nil
}

func (s *Store) CalendarVersion(_ context.Context, termID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	term, ok := s.calTerms[termID]
	if !ok {
		return 0, store.ErrNotFound
	}
	return term.Version, nil
}

func (s *Store) Close() error { return nil }

func idemKey(userID, key string) string { return userID + "\x00" + key }

func (s *Store) ReserveIdempotency(_ context.Context, r store.IdempotencyRecord) (store.IdempotencyRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := idemKey(r.UserID, r.Key)
	if prev, ok := s.idem[k]; ok {
		return *prev, true, nil
	}
	cp := r
	cp.CreatedAt = time.Now()
	s.idem[k] = &cp
	return store.IdempotencyRecord{}, false, nil
}

func (s *Store) FinalizeIdempotency(_ context.Context, key, userID, status string, response []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.idem[idemKey(userID, key)]
	if !ok {
		return store.ErrNotFound
	}
	rec.Status = status
	rec.Response = response
	return nil
}

func (s *Store) CleanupIdempotency(_ context.Context, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, rec := range s.idem {
		if rec.CreatedAt.Before(before) {
			delete(s.idem, k)
		}
	}
	return nil
}

const teacherEvalLogCap = 200

// activeEvalTask returns a queued/running task of userID other than exceptID.
func (s *Store) activeEvalTask(userID, exceptID string) *store.TeacherEvalTask {
	for _, t := range s.evalTasks {
		if t.UserID == userID && t.ID != exceptID && isActiveEvalStatus(t.Status) {
			return t
		}
	}
	return nil
}

func isActiveEvalStatus(status string) bool {
	return status == "queued" || status == "running"
}

func (s *Store) CreateTeacherEvalTask(_ context.Context, t store.TeacherEvalTask, courses []store.TeacherEvalTaskCourse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.evalTasks[t.ID]; dup {
		return fmt.Errorf("store: teacher eval task %s already exists", t.ID)
	}
	seen := make(map[string]bool, len(courses))
	for _, c := range courses {
		if seen[c.CourseKey] {
			return fmt.Errorf("store: duplicate teacher eval course %q", c.CourseKey)
		}
		seen[c.CourseKey] = true
	}
	if isActiveEvalStatus(t.Status) && s.activeEvalTask(t.UserID, "") != nil {
		return store.ErrTeacherEvalTaskActive
	}
	t.ScoreMode = scoreModeOrDefault(t.ScoreMode)
	cp := t
	s.evalTasks[t.ID] = &cp
	rows := make([]store.TeacherEvalTaskCourse, 0, len(courses))
	for _, c := range courses {
		c.TaskID = t.ID
		rows = append(rows, c)
	}
	s.evalCourses[t.ID] = rows
	return nil
}

func (s *Store) GetTeacherEvalTask(_ context.Context, id string) (*store.TeacherEvalTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.evalTasks[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *t
	return &cp, nil
}

func (s *Store) ActiveTeacherEvalTask(_ context.Context, userID string) (*store.TeacherEvalTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.activeEvalTask(userID, "")
	if t == nil {
		return nil, store.ErrNotFound
	}
	cp := *t
	return &cp, nil
}

// ListTeacherEvalTasks returns the user's tasks, newest first (CreatedAt,
// then ID, both descending — the pg ORDER BY).
func (s *Store) ListTeacherEvalTasks(_ context.Context, userID string, limit int) ([]store.TeacherEvalTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var out []store.TeacherEvalTask
	for _, t := range s.evalTasks {
		if t.UserID == userID {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ListRecoverableTeacherEvalTasks returns queued/running tasks, oldest first.
func (s *Store) ListRecoverableTeacherEvalTasks(_ context.Context) ([]store.TeacherEvalTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.TeacherEvalTask
	for _, t := range s.evalTasks {
		if isActiveEvalStatus(t.Status) {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *Store) UpdateTeacherEvalTask(_ context.Context, t store.TeacherEvalTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.evalTasks[t.ID]; !ok {
		return store.ErrNotFound
	}
	if isActiveEvalStatus(t.Status) && s.activeEvalTask(t.UserID, t.ID) != nil {
		return store.ErrTeacherEvalTaskActive
	}
	t.ScoreMode = scoreModeOrDefault(t.ScoreMode)
	cp := t
	s.evalTasks[t.ID] = &cp
	return nil
}

// ListTeacherEvalCourses returns the task's courses by Position ascending.
func (s *Store) ListTeacherEvalCourses(_ context.Context, taskID string) ([]store.TeacherEvalTaskCourse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]store.TeacherEvalTaskCourse(nil), s.evalCourses[taskID]...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out, nil
}

func (s *Store) UpdateTeacherEvalCourse(_ context.Context, c store.TeacherEvalTaskCourse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.evalCourses[c.TaskID]
	for i := range rows {
		if rows[i].CourseKey == c.CourseKey {
			rows[i] = c
			return nil
		}
	}
	return store.ErrNotFound
}

// AppendTeacherEvalLog assigns the next Seq and drops the oldest rows so
// at most teacherEvalLogCap remain for the task.
func (s *Store) AppendTeacherEvalLog(_ context.Context, taskID string, at time.Time, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.evalTasks[taskID]; !ok {
		return store.ErrNotFound
	}
	s.evalLogSeq++
	logs := append(s.evalLogs[taskID], store.TeacherEvalLog{TaskID: taskID, Seq: s.evalLogSeq, At: at, Message: message})
	if len(logs) > teacherEvalLogCap {
		logs = append([]store.TeacherEvalLog(nil), logs[len(logs)-teacherEvalLogCap:]...)
	}
	s.evalLogs[taskID] = logs
	return nil
}

// ListTeacherEvalLogs returns the most recent limit rows, oldest first.
func (s *Store) ListTeacherEvalLogs(_ context.Context, taskID string, limit int) ([]store.TeacherEvalLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	logs := s.evalLogs[taskID]
	if len(logs) > limit {
		logs = logs[len(logs)-limit:]
	}
	return append([]store.TeacherEvalLog(nil), logs...), nil
}

// SetTeacherEvalCourses replaces all course rows of the task. ErrNotFound
// if the task does not exist; each row's TaskID is forced to taskID.
func (s *Store) SetTeacherEvalCourses(_ context.Context, taskID string, courses []store.TeacherEvalTaskCourse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.evalTasks[taskID]; !ok {
		return store.ErrNotFound
	}
	rows := make([]store.TeacherEvalTaskCourse, 0, len(courses))
	for _, c := range courses {
		c.TaskID = taskID
		rows = append(rows, c)
	}
	s.evalCourses[taskID] = rows
	return nil
}

// scoreModeOrDefault maps the empty score mode to "random".
func scoreModeOrDefault(m string) string {
	if m == "" {
		return "random"
	}
	return m
}
