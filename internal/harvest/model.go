package harvest

import (
	"context"
	"time"
)

type Chat struct {
	ID                   int64     `json:"id"`
	Type                 string    `json:"type"`
	Title                string    `json:"title,omitempty"`
	Username             string    `json:"username,omitempty"`
	Display              string    `json:"display"`
	Forum                bool      `json:"forum,omitempty"`
	Pinned               bool      `json:"pinned,omitempty"`
	UnreadCount          int       `json:"unread_count,omitempty"`
	TopMessageID         int       `json:"top_message_id,omitempty"`
	LastMessageAt        time.Time `json:"last_message_at,omitempty"`
	ParticipantsEstimate int       `json:"participants_estimate,omitempty"`
}

type Topic struct {
	ID            int       `json:"id"`
	Title         string    `json:"title,omitempty"`
	TopMessageID  int       `json:"top_message_id,omitempty"`
	LastMessageAt time.Time `json:"last_message_at,omitempty"`
	Pinned        bool      `json:"pinned,omitempty"`
	Closed        bool      `json:"closed,omitempty"`
	Hidden        bool      `json:"hidden,omitempty"`
	UnreadCount   int       `json:"unread_count,omitempty"`
}

type Sender struct {
	ID       int64  `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Username string `json:"username,omitempty"`
	Display  string `json:"display,omitempty"`
	Self     bool   `json:"self,omitempty"`
	Bot      bool   `json:"bot,omitempty"`
}

type SelfProfile struct {
	ID        int64  `json:"id"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Phone     string `json:"phone,omitempty"`
	Display   string `json:"display,omitempty"`
}

type Attachment struct {
	Kind     string `json:"kind"`
	FileName string `json:"file_name,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

type MessageRecord struct {
	Source             string       `json:"source"`
	SourceURL          string       `json:"source_url,omitempty"`
	Chat               Chat         `json:"chat"`
	MessageID          int          `json:"message_id"`
	Date               time.Time    `json:"date"`
	Sender             Sender       `json:"sender,omitempty"`
	Outgoing           bool         `json:"outgoing,omitempty"`
	Kind               string       `json:"kind"`
	Text               string       `json:"text,omitempty"`
	Topic              *Topic       `json:"topic,omitempty"`
	ReplyToMessageID   int          `json:"reply_to_message_id,omitempty"`
	ThreadTopMessageID int          `json:"thread_top_message_id,omitempty"`
	Pinned             bool         `json:"pinned,omitempty"`
	Views              int          `json:"views,omitempty"`
	Links              []string     `json:"links,omitempty"`
	Attachments        []Attachment `json:"attachments,omitempty"`
	RawAction          string       `json:"raw_action,omitempty"`
}

type SyncState struct {
	Chat       Chat      `json:"chat"`
	Topic      *Topic    `json:"topic,omitempty"`
	LastSyncAt time.Time `json:"last_sync_at"`
	LastID     int       `json:"last_id"`
	Records    int       `json:"records"`
	Backfill   *Backfill `json:"backfill,omitempty"`
}

type Backfill struct {
	Active       bool      `json:"active"`
	Complete     bool      `json:"complete,omitempty"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
	CompletedAt  time.Time `json:"completed_at,omitempty"`
	NextOffsetID int       `json:"next_offset_id,omitempty"`
	LatestID     int       `json:"latest_id,omitempty"`
	OldestID     int       `json:"oldest_id,omitempty"`
	Records      int       `json:"records"`
	Batches      int       `json:"batches"`
}

type HistoryOptions struct {
	Limit         int
	BatchSize     int
	MaxBatches    int
	MinID         int
	StartOffsetID int
	All           bool
	TopicID       int
	TopicTitle    string
	Progress      func(HistoryProgress) error
}

type HistoryStats struct {
	Records    int  `json:"records"`
	FirstID    int  `json:"first_id,omitempty"`
	LastID     int  `json:"last_id,omitempty"`
	Batches    int  `json:"batches"`
	FloodWaits int  `json:"flood_waits"`
	Complete   bool `json:"complete,omitempty"`
}

type HistoryProgress struct {
	BatchRecords int  `json:"batch_records"`
	Records      int  `json:"records"`
	FirstID      int  `json:"first_id,omitempty"`
	LastID       int  `json:"last_id,omitempty"`
	Batches      int  `json:"batches"`
	NextOffsetID int  `json:"next_offset_id,omitempty"`
	Done         bool `json:"done,omitempty"`
	FloodWaits   int  `json:"flood_waits"`
}

type HistorySource interface {
	DumpHistory(ctx context.Context, chat string, opts HistoryOptions, emit func(MessageRecord) error) (Chat, HistoryStats, error)
}
