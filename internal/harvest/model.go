package harvest

import (
	"context"
	"time"
)

const (
	DefaultMaxPhotoBytes    int64 = 10 * 1024 * 1024
	DefaultMaxDocumentBytes int64 = 10 * 1024 * 1024
	DefaultMaxAudioBytes    int64 = 50 * 1024 * 1024
	DefaultMaxVideoBytes    int64 = 200 * 1024 * 1024
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
	Kind             string `json:"kind"`
	MediaID          string `json:"media_id,omitempty"`
	FileName         string `json:"file_name,omitempty"`
	MIMEType         string `json:"mime_type,omitempty"`
	Size             int64  `json:"size,omitempty"`
	Title            string `json:"title,omitempty"`
	URL              string `json:"url,omitempty"`
	LocalPath        string `json:"local_path,omitempty"`
	DownloadError    string `json:"download_error,omitempty"`
	DownloadHint     string `json:"download_hint,omitempty"`
	Transcript       string `json:"transcript,omitempty"`
	TranscriptPath   string `json:"transcript_path,omitempty"`
	TranscriptCached bool   `json:"transcript_cached,omitempty"`
	TranscriptError  string `json:"transcript_error,omitempty"`
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
	Limit                 int
	BatchSize             int
	MaxBatches            int
	MinID                 int
	StartOffsetID         int
	All                   bool
	TopicID               int
	TopicTitle            string
	DownloadMedia         bool
	MediaDir              string
	MaxPhotoBytes         int64
	MaxDocumentBytes      int64
	MaxAudioBytes         int64
	MaxVideoBytes         int64
	ManualDownloadCommand string
	TranscribeMedia       bool
	TranscriptDir         string
	TranscribeCommand     string
	VoskCommand           string
	VoskModelPath         string
	VoskGrammarPath       string
	FFmpegCommand         string
	Transcriber           Transcriber
	Progress              func(HistoryProgress) error
}

type Transcriber interface {
	Run(ctx context.Context, inputPath string, outputPath string) (string, error)
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

type OutgoingDayOptions struct {
	Start          time.Time
	End            time.Time
	DialogLimit    int
	IncludeService bool
	History        HistoryOptions
	Progress       func(OutgoingDayProgress) error
}

type OutgoingDayStats struct {
	Records            int       `json:"records"`
	DialogsScanned     int       `json:"dialogs_scanned"`
	DialogsWithRecords int       `json:"dialogs_with_records"`
	DialogsSkipped     int       `json:"dialogs_skipped"`
	DialogErrors       []string  `json:"dialog_errors,omitempty"`
	Attachments        int       `json:"attachments"`
	Transcripts        int       `json:"transcripts"`
	FirstAt            time.Time `json:"first_at,omitempty"`
	LastAt             time.Time `json:"last_at,omitempty"`
	Batches            int       `json:"batches"`
	FloodWaits         int       `json:"flood_waits"`
	Complete           bool      `json:"complete,omitempty"`
}

type OutgoingDayProgress struct {
	Chat       Chat
	Records    int
	Total      int
	Batches    int
	Skipped    bool
	Error      string
	FloodWaits int
}
