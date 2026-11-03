package store

import "time"

// Device grant states (RFC 8628 device authorization).
const (
	DeviceGrantPending  = "pending"
	DeviceGrantApproved = "approved"
	DeviceGrantConsumed = "consumed"
)

// DeviceGrant is one device authorization. The device polls with its device
// code; a user approves by entering the user code on the verification page.
// Only hashes of the codes are stored, never the codes themselves.
type DeviceGrant struct {
	ID             string
	DeviceCodeHash string
	UserCodeHash   string
	ClientName     string
	Platform       string
	Status         string
	UserID         string // set on approval
	Attempts       int    // failed credential attempts against this grant
	Interval       int    // seconds the device must wait between polls
	LastPolledAt   time.Time
	ExpiresAt      time.Time
	CreatedAt      time.Time
}
