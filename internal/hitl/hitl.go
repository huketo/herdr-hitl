// Package hitl holds the human-in-the-loop domain model: the question an agent
// asks, the answer a human gives, and the broker that pairs them up.
//
// Nothing in this package knows about Telegram, Discord, sockets, or the CLI.
// Transports plug in through the Poster interface.
package hitl

import (
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits shared by every transport. Individual transports clamp further.
const (
	// MaxTitleLen bounds the one-line summary shown in message headers and
	// terminal notifications.
	MaxTitleLen = 200
	// MaxBodyLen bounds the question body. Telegram caps a text message at
	// 4096 UTF-16 code units and Discord at 2000 characters; transports
	// truncate to their own limit, this is the outer sanity bound.
	MaxBodyLen = 100_000
	// MaxChoices bounds the number of predefined answers. Discord string
	// selects allow 25 options, which is the tighter of the two transports.
	MaxChoices = 25
	// MaxChoiceLabelLen is the shortest label limit across transports
	// (Discord buttons: 80 characters).
	MaxChoiceLabelLen = 80
	// MaxChoiceIDLen keeps generated callback payloads inside the Telegram
	// 64-byte callback_data budget once the request prefix is added.
	MaxChoiceIDLen = 32
	// MaxAttachments bounds a single request's attachment count.
	MaxAttachments = 10
	// MaxAttachmentBytes is the smallest per-file ceiling across transports
	// (Discord non-Nitro: 10 MiB).
	MaxAttachmentBytes = 10 << 20
)

// Errors returned by the domain layer and mapped to CLI exit codes.
var (
	// ErrTimeout means nobody answered before the deadline elapsed.
	ErrTimeout = errors.New("hitl: request timed out")
	// ErrCanceled means the request was withdrawn before an answer arrived.
	ErrCanceled = errors.New("hitl: request canceled")
	// ErrUnknownRequest means the referenced request is not pending.
	ErrUnknownRequest = errors.New("hitl: unknown request")
	// ErrAlreadyAnswered means a second answer lost the race.
	ErrAlreadyAnswered = errors.New("hitl: request already answered")
	// ErrNoTransport means no configured transport could carry the request.
	ErrNoTransport = errors.New("hitl: no transport available")
)

// Choice is one predefined answer offered to the human.
type Choice struct {
	// ID is the stable machine value handed back to the agent. It is what
	// scripts branch on, so it never contains transport formatting.
	ID string `json:"id"`
	// Label is the human-facing button text.
	Label string `json:"label"`
	// Style is an optional presentation hint: "", "primary", "danger".
	Style string `json:"style,omitempty"`
}

// Choice style hints. Transports map these onto their own button styles and
// ignore values they cannot express.
const (
	StyleDefault = ""
	StylePrimary = "primary"
	StyleDanger  = "danger"
)

// AttachmentKind decides whether a transport uploads a file as a viewable
// image or as a plain document.
type AttachmentKind string

// Attachment kinds.
const (
	KindImage    AttachmentKind = "image"
	KindDocument AttachmentKind = "document"
)

// Attachment is a local file shipped alongside the question.
type Attachment struct {
	// Path is the absolute path on the machine running the daemon.
	Path string `json:"path"`
	// Filename is the name shown to the human.
	Filename string `json:"filename"`
	// Kind selects the upload style.
	Kind AttachmentKind `json:"kind"`
	// MediaType is the MIME type, e.g. "text/markdown".
	MediaType string `json:"media_type"`
	// Size is the file size in bytes.
	Size int64 `json:"size"`
	// Caption is optional descriptive text / alt text.
	Caption string `json:"caption,omitempty"`
}

// imageExts and documentTypes drive attachment classification. Anything not
// recognised as an image is uploaded as a document, which is the safe default:
// a document always renders, an image may be rejected.
var imageExts = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

var documentTypes = map[string]string{
	".md":       "text/markdown",
	".markdown": "text/markdown",
	".html":     "text/html",
	".htm":      "text/html",
	".txt":      "text/plain",
	".log":      "text/plain",
	".json":     "application/json",
	".yaml":     "application/yaml",
	".yml":      "application/yaml",
	".toml":     "application/toml",
	".patch":    "text/x-diff",
	".diff":     "text/x-diff",
	".csv":      "text/csv",
	".pdf":      "application/pdf",
}

// NewAttachment stats path and classifies it. It rejects directories,
// unreadable files, and files above MaxAttachmentBytes so that a bad argument
// fails in the CLI rather than halfway through an upload.
func NewAttachment(path string) (Attachment, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Attachment{}, fmt.Errorf("resolve attachment path %q: %w", path, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Attachment{}, fmt.Errorf("attachment %q: %w", path, err)
	}
	if info.IsDir() {
		return Attachment{}, fmt.Errorf("attachment %q: is a directory", path)
	}
	if info.Size() == 0 {
		return Attachment{}, fmt.Errorf("attachment %q: file is empty", path)
	}
	if info.Size() > MaxAttachmentBytes {
		return Attachment{}, fmt.Errorf("attachment %q: %d bytes exceeds the %d byte limit",
			path, info.Size(), int64(MaxAttachmentBytes))
	}

	ext := strings.ToLower(filepath.Ext(abs))
	att := Attachment{
		Path:     abs,
		Filename: filepath.Base(abs),
		Size:     info.Size(),
	}
	switch {
	case imageExts[ext] != "":
		att.Kind = KindImage
		att.MediaType = imageExts[ext]
	case documentTypes[ext] != "":
		att.Kind = KindDocument
		att.MediaType = documentTypes[ext]
	default:
		att.Kind = KindDocument
		att.MediaType = mime.TypeByExtension(ext)
		if att.MediaType == "" {
			att.MediaType = "application/octet-stream"
		}
	}
	return att, nil
}

// Origin records where a question came from so the human can tell two
// concurrent agents apart in the same chat.
type Origin struct {
	Host           string `json:"host,omitempty"`
	User           string `json:"user,omitempty"`
	Cwd            string `json:"cwd,omitempty"`
	Agent          string `json:"agent,omitempty"`
	WorkspaceID    string `json:"workspace_id,omitempty"`
	WorkspaceLabel string `json:"workspace_label,omitempty"`
	TabID          string `json:"tab_id,omitempty"`
	PaneID         string `json:"pane_id,omitempty"`
}

// Label renders the most specific human-readable origin available.
func (o Origin) Label() string {
	parts := make([]string, 0, 3)
	if o.Agent != "" {
		parts = append(parts, o.Agent)
	}
	switch {
	case o.WorkspaceLabel != "":
		parts = append(parts, o.WorkspaceLabel)
	case o.Cwd != "":
		parts = append(parts, filepath.Base(o.Cwd))
	}
	if o.Host != "" {
		parts = append(parts, o.Host)
	}
	return strings.Join(parts, " · ")
}

// Request is a question awaiting a human decision, or — when Notice is set —
// a message that expects nothing back.
type Request struct {
	ID            string        `json:"id"`
	Title         string        `json:"title"`
	Body          string        `json:"body"`
	Choices       []Choice      `json:"choices,omitempty"`
	AllowFreeText bool          `json:"allow_free_text"`
	Attachments   []Attachment  `json:"attachments,omitempty"`
	Timeout       time.Duration `json:"timeout"`
	// Notice marks a fire-and-forget message. Transports must render it
	// without buttons, without a reply prompt, and without inviting an
	// answer that nothing is waiting for.
	Notice bool `json:"notice,omitempty"`
	// Transports names the transports to fan out to. Empty means "every
	// enabled transport".
	Transports []string  `json:"transports,omitempty"`
	Origin     Origin    `json:"origin"`
	CreatedAt  time.Time `json:"created_at"`
}

// WantsAnswer reports whether a human is expected to respond.
func (r *Request) WantsAnswer() bool { return !r.Notice }

// Deadline reports when the request expires, and whether it has one at all.
func (r *Request) Deadline() (time.Time, bool) {
	if r.Timeout <= 0 {
		return time.Time{}, false
	}
	return r.CreatedAt.Add(r.Timeout), true
}

// ChoiceByID finds a predefined choice.
func (r *Request) ChoiceByID(id string) (Choice, bool) {
	for _, c := range r.Choices {
		if c.ID == id {
			return c, true
		}
	}
	return Choice{}, false
}

// Validate enforces the invariants every transport relies on.
func (r *Request) Validate() error {
	if strings.TrimSpace(r.Body) == "" && strings.TrimSpace(r.Title) == "" {
		return errors.New("hitl: request needs a title or a body")
	}
	if utf8.RuneCountInString(r.Title) > MaxTitleLen {
		return fmt.Errorf("hitl: title exceeds %d characters", MaxTitleLen)
	}
	if utf8.RuneCountInString(r.Body) > MaxBodyLen {
		return fmt.Errorf("hitl: body exceeds %d characters", MaxBodyLen)
	}
	if len(r.Choices) > MaxChoices {
		return fmt.Errorf("hitl: %d choices exceeds the limit of %d", len(r.Choices), MaxChoices)
	}
	if r.WantsAnswer() && len(r.Choices) == 0 && !r.AllowFreeText {
		return errors.New("hitl: request offers no choices and forbids free text")
	}
	if r.Notice && (len(r.Choices) > 0 || r.AllowFreeText) {
		return errors.New("hitl: a notice must not offer choices or a reply")
	}
	seen := make(map[string]struct{}, len(r.Choices))
	for i, c := range r.Choices {
		if strings.TrimSpace(c.ID) == "" {
			return fmt.Errorf("hitl: choice %d has an empty id", i+1)
		}
		if utf8.RuneCountInString(c.ID) > MaxChoiceIDLen {
			return fmt.Errorf("hitl: choice id %q exceeds %d characters", c.ID, MaxChoiceIDLen)
		}
		if strings.TrimSpace(c.Label) == "" {
			return fmt.Errorf("hitl: choice %q has an empty label", c.ID)
		}
		if utf8.RuneCountInString(c.Label) > MaxChoiceLabelLen {
			return fmt.Errorf("hitl: choice label %q exceeds %d characters", c.Label, MaxChoiceLabelLen)
		}
		if _, dup := seen[c.ID]; dup {
			return fmt.Errorf("hitl: duplicate choice id %q", c.ID)
		}
		seen[c.ID] = struct{}{}
		switch c.Style {
		case StyleDefault, StylePrimary, StyleDanger:
		default:
			return fmt.Errorf("hitl: choice %q has unknown style %q", c.ID, c.Style)
		}
	}
	if len(r.Attachments) > MaxAttachments {
		return fmt.Errorf("hitl: %d attachments exceeds the limit of %d",
			len(r.Attachments), MaxAttachments)
	}
	if r.Timeout < 0 {
		return errors.New("hitl: timeout must not be negative")
	}
	return nil
}

// Status is the terminal state of a request.
type Status string

// Request outcomes.
const (
	StatusAnswered Status = "answered"
	StatusTimeout  Status = "timeout"
	StatusCanceled Status = "canceled"
)

// Responder identifies the human who answered.
type Responder struct {
	Transport string `json:"transport,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	Username  string `json:"username,omitempty"`
}

// Display renders the responder for message footers.
func (r Responder) Display() string {
	switch {
	case r.Username != "":
		return r.Username
	case r.UserID != "":
		return r.UserID
	case r.Transport != "":
		return r.Transport
	default:
		return "unknown"
	}
}

// Answer is the resolution of a Request.
type Answer struct {
	RequestID string `json:"request_id"`
	Status    Status `json:"status"`
	// ChoiceID is set when the human picked a predefined choice.
	ChoiceID string `json:"choice_id,omitempty"`
	// ChoiceLabel mirrors the chosen label for display.
	ChoiceLabel string `json:"choice_label,omitempty"`
	// Text is the answer as plain text: the free-text reply, or the label of
	// the chosen option. Always populated for StatusAnswered.
	Text       string    `json:"text"`
	Responder  Responder `json:"responder"`
	AnsweredAt time.Time `json:"answered_at"`
	// Reason explains a non-answered status.
	Reason string `json:"reason,omitempty"`
}

// Answered reports whether a human actually decided.
func (a *Answer) Answered() bool { return a != nil && a.Status == StatusAnswered }

// Err maps a non-answered outcome onto a sentinel error.
func (a *Answer) Err() error {
	switch {
	case a == nil:
		return ErrUnknownRequest
	case a.Status == StatusAnswered:
		return nil
	case a.Status == StatusTimeout:
		return ErrTimeout
	default:
		return ErrCanceled
	}
}
