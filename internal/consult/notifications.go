package consult

import "time"

type NotificationDisposition string

const (
	NotificationPending    NotificationDisposition = "pending"
	NotificationClaimed    NotificationDisposition = "claimed"
	NotificationSuppressed NotificationDisposition = "suppressed"
	NotificationDelivered  NotificationDisposition = "delivered"
	NotificationFailed     NotificationDisposition = "failed"
)

// Notification is the durable state of one transition's best-effort delivery.
// Separate timestamps retain the complete ledger history as its disposition advances.
type Notification struct {
	Disposition  NotificationDisposition `json:"disposition"`
	Message      string                  `json:"message,omitempty"`
	PendingAt    time.Time               `json:"pending_at,omitzero"`
	ClaimedAt    time.Time               `json:"claimed_at,omitzero"`
	SuppressedAt time.Time               `json:"suppressed_at,omitzero"`
	DeliveredAt  time.Time               `json:"delivered_at,omitzero"`
	FailedAt     time.Time               `json:"failed_at,omitzero"`
}
