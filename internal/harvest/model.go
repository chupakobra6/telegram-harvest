package harvest

import (
	"context"
	"time"
)

const (
	DefaultMaxPhotoBytes            int64 = 10 * 1024 * 1024
	DefaultMaxDocumentBytes         int64 = 10 * 1024 * 1024
	DefaultMaxAudioBytes            int64 = 50 * 1024 * 1024
	DefaultMaxVideoBytes            int64 = 200 * 1024 * 1024
	DefaultMaxGenericVideoBytes     int64 = 80 * 1024 * 1024
	DefaultMaxGenericVideoSeconds         = 6 * 60
	DefaultMaxGenericVideoShortSide       = 1080
	DefaultMaxGenericVideoLongSide        = 1920
	VideoTranscribePhone                  = "phone"
	VideoTranscribeAll                    = "all"
	VideoTranscribeOff                    = "off"
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
	Kind             string  `json:"kind"`
	MediaID          string  `json:"media_id,omitempty"`
	FileName         string  `json:"file_name,omitempty"`
	MIMEType         string  `json:"mime_type,omitempty"`
	Size             int64   `json:"size,omitempty"`
	DurationSeconds  float64 `json:"duration_seconds,omitempty"`
	Width            int     `json:"width,omitempty"`
	Height           int     `json:"height,omitempty"`
	Title            string  `json:"title,omitempty"`
	URL              string  `json:"url,omitempty"`
	LocalPath        string  `json:"local_path,omitempty"`
	DownloadError    string  `json:"download_error,omitempty"`
	DownloadHint     string  `json:"download_hint,omitempty"`
	Transcript       string  `json:"transcript,omitempty"`
	TranscriptPath   string  `json:"transcript_path,omitempty"`
	TranscriptCached bool    `json:"transcript_cached,omitempty"`
	TranscriptError  string  `json:"transcript_error,omitempty"`
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
	Start                 time.Time
	End                   time.Time
	All                   bool
	TopicID               int
	TopicTitle            string
	DownloadMedia         bool
	MediaDir              string
	MaxPhotoBytes         int64
	MaxDocumentBytes      int64
	MaxAudioBytes         int64
	MaxVideoBytes         int64
	MaxGenericVideoBytes  int64
	ManualDownloadCommand string
	TranscribeMedia       bool
	VideoTranscribeMode   string
	TranscriptDir         string
	TranscribeCommand     string
	VoskCommand           string
	VoskModelPath         string
	VoskGrammarPath       string
	FFmpegCommand         string
	Transcriber           Transcriber
	Progress              func(HistoryProgress) error
	ASRLog                func(ASRLogEvent) error
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
	Start                     time.Time
	End                       time.Time
	DialogLimit               int
	IncludeService            bool
	AdditionalSenderIDsByChat map[int64][]int64
	History                   HistoryOptions
	Progress                  func(OutgoingDayProgress) error
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

type ASRLogEvent struct {
	At                  time.Time `json:"at"`
	Action              string    `json:"action"`
	Stage               string    `json:"stage,omitempty"`
	Reason              string    `json:"reason,omitempty"`
	Error               string    `json:"error,omitempty"`
	Engine              string    `json:"engine,omitempty"`
	Date                time.Time `json:"date,omitempty"`
	Chat                Chat      `json:"chat"`
	MessageID           int       `json:"message_id,omitempty"`
	AttachmentIndex     int       `json:"attachment_index"`
	Kind                string    `json:"kind,omitempty"`
	MediaID             string    `json:"media_id,omitempty"`
	FileName            string    `json:"file_name,omitempty"`
	MIMEType            string    `json:"mime_type,omitempty"`
	Size                int64     `json:"size,omitempty"`
	DurationSeconds     float64   `json:"duration_seconds,omitempty"`
	Width               int       `json:"width,omitempty"`
	Height              int       `json:"height,omitempty"`
	TranscriptPath      string    `json:"transcript_path,omitempty"`
	TranscriptCached    bool      `json:"transcript_cached,omitempty"`
	DownloadSeconds     float64   `json:"download_seconds,omitempty"`
	FFmpegSeconds       float64   `json:"ffmpeg_seconds,omitempty"`
	ASRSeconds          float64   `json:"asr_seconds,omitempty"`
	TotalSeconds        float64   `json:"total_seconds,omitempty"`
	InputBytes          int64     `json:"input_bytes,omitempty"`
	WAVBytes            int64     `json:"wav_bytes,omitempty"`
	WAVDurationSeconds  float64   `json:"wav_duration_seconds,omitempty"`
	TranscriptBytes     int64     `json:"transcript_bytes,omitempty"`
	RealTimeFactor      float64   `json:"real_time_factor,omitempty"`
	VideoTranscribeMode string    `json:"video_transcribe_mode,omitempty"`
}
