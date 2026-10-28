// Package store defines the persistence contract for loveaced. The pg
// implementation is the production store; the mem implementation backs unit
// tests. DESIGN.md §0: PostgreSQL + pgx v5, no Redis.
package store

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/Sibuxiangx/loveace-go/server/internal/vault"
)

// Credential lifecycle states (DESIGN.md §6). invalid stops ALL automatic
// logins until the user re-enters credentials.
const (
	CredUnverified = "unverified"
	CredValid      = "valid"
	CredInvalid    = "invalid"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("store: not found")

// User is one LoveACE account. The school account number is stored only as
// a keyed hash (AccountHash); the reversible form lives in the vault blob.
type User struct {
	ID              string
	AccountHash     string
	CredentialState string
	CreatedAt       time.Time
}

// Device is one logged-in client.
type Device struct {
	ID        string
	UserID    string
	Name      string
	Platform  string
	CreatedAt time.Time
	RevokedAt *time.Time
}

// RefreshToken is the at-rest row of one refresh secret (hash only).
type RefreshToken struct {
	Hash      string
	UserID    string
	DeviceID  string
	ExpiresAt time.Time
	RotatedTo string // non-empty once rotated away
	Revoked   bool
	CreatedAt time.Time
}

// Credential is one user's encrypted credential blob: account number and
// passwords in one vault.Stored JSON payload (reversible, §6).
type Credential struct {
	UserID      string
	Blob        vault.Stored
	Fingerprint string
	UpdatedAt   time.Time
}

// PoolSlot is one pooled EC gateway session at rest (§7.7 boundary 11):
// the gateway-cookie snapshot, sealed with the vault (AAD domain
// "ecpool-slot"). Never contains a CAS identity.
type PoolSlot struct {
	ID             string
	Blob           vault.Stored
	DepositedAt    time.Time
	LastVerifiedAt time.Time
}

// UserSnapshot is one cold-evicted user session at rest (§9.1): the
// session cookies sealed with the vault (AAD domain "user-session").
type UserSnapshot struct {
	UserID  string
	Blob    vault.Stored
	SavedAt time.Time
}

// Announcement is an operator-authored notice (§11.2).
type Announcement struct {
	ID          string
	Level       string // info | warning | critical
	BodyMD      string
	Audience    string // pre_login | post_login | both
	Pinned      bool
	RequiresAck bool
	Active      bool
	StartsAt    time.Time
	EndsAt      *time.Time
}

// CalendarTerm is one academic term (§11.1). Version bumps on publish.
type CalendarTerm struct {
	ID              string // term code, e.g. 2026-2027-1-1
	FirstWeekMonday time.Time
	Weeks           int
	Version         int
}

// CalendarRule is one day override: holiday/makeup/exam/vacation.
// Makeup carries {FollowWeek, FollowWeekday}: "this day follows that
// week's weekday timetable". Rules are authored as drafts and become the
// published set when the term's calendar is published.
type CalendarRule struct {
	ID            string
	TermID        string
	Day           time.Time
	Kind          string
	FollowWeek    int
	FollowWeekday int
	Status        string // draft | published
	Version       int
}

// IdempotencyRecord tracks one write-operation attempt keyed by the
// client's Idempotency-Key (M5): retries replay the stored outcome
// instead of re-executing — a retried payment must never charge twice.
// Status: pending (reserved, executing) | done | failed | unknown (the
// school gave no terminal answer; the client reconciles).
type IdempotencyRecord struct {
	Key         string
	UserID      string
	Op          string
	RequestHash string
	Status      string
	Response    []byte // stored JSON outcome to replay; nil while pending
	CreatedAt   time.Time
}

// AuditEvent is one append-only audit record. Never contains credentials.
type AuditEvent struct {
	At       time.Time
	UserID   string
	DeviceID string
	Event    string
	Detail   string
}

// TeacherEvalTask is one background auto-evaluation run for one user.
// Status: queued | running | completed | failed | cancelled | interrupted.
// Zero times mean unset.
type TeacherEvalTask struct {
	ID              string
	UserID          string
	DeviceID        string
	Status          string
	ErrorKind       string
	ErrorMessage    string
	CancelRequested bool
	CourseTotal     int
	ScoreMode       string // "random" | "max"; empty reads as "random"
	CreatedAt       time.Time
	StartedAt       time.Time
	FinishedAt      time.Time
}

// TeacherEvalTaskCourse is one course inside a task.
// Status: pending | preparing | waiting | submitting | verifying | success | failed | cancelled | skipped.
// Zero times mean unset.
type TeacherEvalTaskCourse struct {
	TaskID         string
	CourseKey      string
	Name           string
	Teacher        string
	Position       int
	Status         string
	ErrorKind      string
	Message        string
	SubmitAttempts int
	SubmittedAt    time.Time
	UpdatedAt      time.Time
}

// TeacherEvalLog is one sanitized progress line for a task.
type TeacherEvalLog struct {
	TaskID  string
	Seq     int64
	At      time.Time
	Message string
}

// ErrTeacherEvalTaskActive means the user already has a queued or running
// auto-evaluation task.
var ErrTeacherEvalTaskActive = errors.New("store: teacher eval task already active")

// Store is the persistence contract.
type Store interface {
	GetOrCreateUser(ctx context.Context, accountHash string) (*User, bool, error)
	GetUser(ctx context.Context, userID string) (*User, error)

	UpsertCredential(ctx context.Context, cred Credential) error
	GetCredential(ctx context.Context, userID string) (*Credential, error)
	SetCredentialState(ctx context.Context, userID, state string) error
	// ListCredentialUserIDs returns up to limit users holding stored
	// credentials, most useful first (pool refill candidates, §7.1).
	ListCredentialUserIDs(ctx context.Context, limit int) ([]string, error)

	CreateDevice(ctx context.Context, userID, name, platform string) (*Device, error)
	ListDevices(ctx context.Context, userID string) ([]Device, error)
	GetDevice(ctx context.Context, userID, deviceID string) (*Device, error)
	RevokeDevice(ctx context.Context, userID, deviceID string) error

	InsertRefreshToken(ctx context.Context, t RefreshToken) error
	GetRefreshToken(ctx context.Context, hash string) (*RefreshToken, error)
	RotateRefreshToken(ctx context.Context, oldHash, newHash string, newExpiresAt time.Time) error
	RevokeDeviceTokens(ctx context.Context, deviceID string) error

	// PruneRotatedRefreshTokens deletes superseded (rotated_to != "")
	// refresh-token rows created before the cutoff. The successor chain
	// keeps reuse detection working within the window; expired chains are
	// dead weight.
	PruneRotatedRefreshTokens(ctx context.Context, before time.Time) error

	AppendAudit(ctx context.Context, e AuditEvent) error

	PutPoolSlot(ctx context.Context, slot PoolSlot) error
	DeletePoolSlot(ctx context.Context, slotID string) error
	ListPoolSlots(ctx context.Context) ([]PoolSlot, error)

	PutUserSnapshot(ctx context.Context, snap UserSnapshot) error
	GetUserSnapshot(ctx context.Context, userID string) (*UserSnapshot, error)
	DeleteUserSnapshot(ctx context.Context, userID string) error

	CreateAnnouncement(ctx context.Context, a Announcement) error
	ListAnnouncements(ctx context.Context, activeOnly bool) ([]Announcement, error)
	DeactivateAnnouncement(ctx context.Context, id string) error
	AckAnnouncement(ctx context.Context, userID, announcementID string) error
	ListAcks(ctx context.Context, userID string) (map[string]time.Time, error)

	UpsertCalendarTerm(ctx context.Context, t CalendarTerm) error
	ListCalendarTerms(ctx context.Context) ([]CalendarTerm, error)
	AddCalendarRule(ctx context.Context, r CalendarRule) error
	ListCalendarRules(ctx context.Context, termID, status string) ([]CalendarRule, error)
	DeleteCalendarRule(ctx context.Context, ruleID string) error
	// PublishCalendar atomically promotes the term's draft rules to
	// published at a new version, retiring older published rules, and
	// returns the new version.
	PublishCalendar(ctx context.Context, termID string) (int, error)
	CalendarVersion(ctx context.Context, termID string) (int, error)

	// ReserveIdempotency atomically claims (key, userID): existed=false
	// means this caller owns the execution; existed=true means prev holds
	// the earlier record (pending/done/failed/unknown).
	ReserveIdempotency(ctx context.Context, r IdempotencyRecord) (prev IdempotencyRecord, existed bool, err error)
	FinalizeIdempotency(ctx context.Context, key, userID, status string, response []byte) error
	CleanupIdempotency(ctx context.Context, before time.Time) error

	// Teacher auto-evaluation (migration 0006). Not-found is ErrNotFound.
	// CreateTeacherEvalTask atomically inserts the task and its courses;
	// ErrTeacherEvalTaskActive if the user already has a queued/running task.
	CreateTeacherEvalTask(ctx context.Context, t TeacherEvalTask, courses []TeacherEvalTaskCourse) error
	GetTeacherEvalTask(ctx context.Context, id string) (*TeacherEvalTask, error)
	ActiveTeacherEvalTask(ctx context.Context, userID string) (*TeacherEvalTask, error)
	// ListTeacherEvalTasks returns the newest first; limit<=0 means 20, cap 100.
	ListTeacherEvalTasks(ctx context.Context, userID string, limit int) ([]TeacherEvalTask, error)
	// ListRecoverableTeacherEvalTasks returns queued or running tasks (restart recovery).
	ListRecoverableTeacherEvalTasks(ctx context.Context) ([]TeacherEvalTask, error)
	UpdateTeacherEvalTask(ctx context.Context, t TeacherEvalTask) error
	// ListTeacherEvalCourses returns the courses ordered by Position ascending.
	ListTeacherEvalCourses(ctx context.Context, taskID string) ([]TeacherEvalTaskCourse, error)
	UpdateTeacherEvalCourse(ctx context.Context, c TeacherEvalTaskCourse) error
	// SetTeacherEvalCourses replaces all course rows of the task in one
	// transaction (each row's TaskID is forced to taskID). An empty slice
	// clears them. ErrNotFound if the task does not exist.
	SetTeacherEvalCourses(ctx context.Context, taskID string, courses []TeacherEvalTaskCourse) error
	// AppendTeacherEvalLog assigns Seq and keeps at most 200 rows per task.
	AppendTeacherEvalLog(ctx context.Context, taskID string, at time.Time, message string) error
	// ListTeacherEvalLogs returns the most recent limit rows, oldest first; limit<=0 means 50.
	ListTeacherEvalLogs(ctx context.Context, taskID string, limit int) ([]TeacherEvalLog, error)

	Close() error
}

// NewID returns a random RFC-4122 v4 UUID string.
func NewID() string {
	var b [16]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	b[6] = (b[6] & 0x0F) | 0x40
	b[8] = (b[8] & 0x3F) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
