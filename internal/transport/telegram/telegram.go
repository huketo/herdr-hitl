// Package telegram delivers questions over the Telegram Bot API and pushes
// human answers back into the broker.
//
// Exactly one process may poll a bot token: getUpdates deletes what it reads
// from a single per-token queue, so a second poller both steals answers and
// makes the Bot API reject one of the two with HTTP 409. That is why this
// transport lives in the resident daemon and never in the CLI.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/huketo/herdr-hitl/internal/config"
	"github.com/huketo/herdr-hitl/internal/hitl"
	"github.com/huketo/herdr-hitl/internal/transport"
)

// Compile-time proof that this transport satisfies the daemon's contract.
var _ transport.Transport = (*Transport)(nil)

const (
	// pollTimeout is both the long-poll timeout and the HTTP client timeout.
	// The Bot API clamps the server-side wait to 50s, so 70s leaves margin
	// for the response to come back before the client gives up.
	pollTimeout = 70 * time.Second
	// startupTimeout bounds the deleteWebhook and getMe handshake.
	startupTimeout = 15 * time.Second
)

// posted records a question we put in a chat, so replies and button presses
// can be traced back to the request they answer.
type posted struct {
	requestID string
	chatID    int64
	messageID int
	// text is the rendered question, reused by Settle to append the outcome.
	text string
	// allowFree marks requests that accept a plain text reply.
	allowFree bool
	// expiresAt prunes entries the broker never settles, such as the
	// fire-and-forget notices sent by Broker.Notify. Zero means never.
	expiresAt time.Time
}

// Transport is a Telegram Bot API backend.
type Transport struct {
	cfg      config.Telegram
	resolver hitl.Resolver
	log      *slog.Logger
	api      *bot.Bot

	// chatID is the configured destination, either an int64 or an @name.
	chatID    any
	chatLabel string
	// allowed is the AllowedUserIDs set, empty when anyone in the chat may
	// answer.
	allowed map[string]struct{}

	mu        sync.Mutex
	username  string
	posted    map[string]*posted
	byMessage map[int]string
	cancel    context.CancelFunc
	done      chan struct{}

	// now is swapped in tests to make pruning deterministic.
	now func() time.Time
}

// New builds a Telegram transport. It performs no network I/O; the connection
// is opened by Start.
func New(cfg config.Telegram, resolver hitl.Resolver, log *slog.Logger) (*Transport, error) {
	if resolver == nil {
		return nil, errors.New("telegram: resolver is required")
	}
	if strings.TrimSpace(cfg.BotToken) == "" {
		return nil, errors.New("telegram: bot_token is required")
	}
	if strings.TrimSpace(cfg.ChatID) == "" {
		return nil, errors.New("telegram: chat_id is required")
	}
	if log == nil {
		log = slog.Default()
	}

	t := &Transport{
		cfg:       cfg,
		resolver:  resolver,
		log:       log.With("transport", config.TransportTelegram),
		chatLabel: cfg.ChatID,
		posted:    make(map[string]*posted),
		byMessage: make(map[int]string),
		now:       time.Now,
	}
	t.chatID = parseChatID(cfg.ChatID)
	t.allowed = make(map[string]struct{}, len(cfg.AllowedUserIDs))
	for _, id := range cfg.AllowedUserIDs {
		if id = strings.TrimSpace(id); id != "" {
			t.allowed[strings.ToLower(strings.TrimPrefix(id, "@"))] = struct{}{}
		}
	}

	opts := []bot.Option{
		// getMe belongs in Start, where a failure can be reported as a
		// transport that would not come up rather than a constructor error.
		bot.WithSkipGetMe(),
		bot.WithAllowedUpdates(bot.AllowedUpdates{
			models.AllowedUpdateMessage,
			models.AllowedUpdateCallbackQuery,
		}),
		bot.WithHTTPClient(pollTimeout, &http.Client{Timeout: pollTimeout}),
		bot.WithDefaultHandler(t.handleUpdate),
		bot.WithErrorsHandler(t.handleAPIError),
	}
	if base := strings.TrimSpace(cfg.APIBase); base != "" {
		opts = append(opts, bot.WithServerURL(strings.TrimRight(base, "/")))
	}

	api, err := bot.New(cfg.BotToken, opts...)
	if err != nil {
		return nil, fmt.Errorf("telegram: create bot: %w", err)
	}
	t.api = api
	return t, nil
}

// parseChatID keeps numeric ids numeric; @channelusername stays a string.
func parseChatID(raw string) any {
	if n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil {
		return n
	}
	return strings.TrimSpace(raw)
}

// Name identifies the transport in configuration and CLI flags.
func (t *Transport) Name() string { return config.TransportTelegram }

// Start clears any webhook, validates the token, and begins long polling. It
// returns once the bot is live; the receive loop runs until ctx is done or
// Close is called.
func (t *Transport) Start(ctx context.Context) error {
	initCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()

	// getUpdates fails with 409 for as long as a webhook is registered, and a
	// leftover webhook from an earlier setup is a common way to get there.
	if _, err := t.api.DeleteWebhook(initCtx, &bot.DeleteWebhookParams{}); err != nil {
		return fmt.Errorf("telegram: delete webhook: %w", err)
	}
	me, err := t.api.GetMe(initCtx)
	if err != nil {
		return fmt.Errorf("telegram: get me: %w", err)
	}

	loopCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})

	t.mu.Lock()
	if t.cancel != nil {
		t.mu.Unlock()
		stop()
		return errors.New("telegram: already started")
	}
	t.username = me.Username
	t.cancel = stop
	t.done = done
	t.mu.Unlock()

	go func() {
		defer close(done)
		t.api.Start(loopCtx)
	}()

	t.log.Info("telegram transport live", "bot", me.Username, "chat", t.chatLabel)
	return nil
}

// Close stops the receive loop. It is idempotent and safe before Start.
func (t *Transport) Close() error {
	t.mu.Lock()
	cancel, done := t.cancel, t.done
	t.cancel, t.done = nil, nil
	t.mu.Unlock()

	if cancel == nil {
		return nil
	}
	cancel()
	<-done
	return nil
}

// Describe summarises the connection for `herdr-hitl doctor`. It never
// includes the bot token.
func (t *Transport) Describe() string {
	t.mu.Lock()
	username := t.username
	t.mu.Unlock()
	if username == "" {
		return fmt.Sprintf("telegram: chat %s (not connected)", t.chatLabel)
	}
	return fmt.Sprintf("telegram: @%s -> chat %s", username, t.chatLabel)
}

// handleAPIError funnels library-level errors into slog. A 409 means another
// process is polling the same token: retrying would starve both pollers and
// neither would ever see an answer, so the loop stops instead.
func (t *Transport) handleAPIError(err error) {
	if errors.Is(err, bot.ErrorConflict) {
		t.log.Error("another process is polling this bot token; stopping the telegram receive loop", "error", err)
		t.mu.Lock()
		cancel := t.cancel
		t.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return
	}
	t.log.Warn("telegram api error", "error", err)
}

// Post delivers req. Attachments go first so the question, with its keyboard,
// is the last thing in the chat.
func (t *Transport) Post(ctx context.Context, req *hitl.Request) error {
	var failed []string
	for _, att := range req.Attachments {
		if err := t.sendAttachment(ctx, att); err != nil {
			// A rejected upload must not cost the human the question; the
			// footer names what is missing instead.
			t.log.Warn("attachment upload failed", "request_id", req.ID, "file", att.Filename, "error", err)
			failed = append(failed, att.Filename)
		}
	}

	q := composeQuestion(req, failed)
	if q.Overflow {
		if err := t.sendBody(ctx, req); err != nil {
			t.log.Warn("body attachment upload failed", "request_id", req.ID, "error", err)
		}
	}

	params := &bot.SendMessageParams{
		ChatID:             t.chatID,
		Text:               q.Text,
		ParseMode:          models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: bot.True()},
	}
	if kb := keyboard(req); kb != nil {
		params.ReplyMarkup = kb
	}

	msg, err := t.api.SendMessage(ctx, params)
	if err != nil {
		return fmt.Errorf("telegram: send question: %w", err)
	}

	t.remember(&posted{
		requestID: req.ID,
		chatID:    msg.Chat.ID,
		messageID: msg.ID,
		text:      q.Text,
		allowFree: req.AllowFreeText,
		expiresAt: expiryOf(req),
	})
	return nil
}

// expiryOf reports when a tracked question may be forgotten. Requests without
// a deadline wait forever by design, so they are never pruned.
func expiryOf(req *hitl.Request) time.Time {
	deadline, ok := req.Deadline()
	if !ok {
		return time.Time{}
	}
	// A grace period keeps the entry alive across the broker's settle pass.
	return deadline.Add(time.Minute)
}

// remember stores a posted question and prunes anything the broker will never
// settle, such as the notices sent by Broker.Notify.
func (t *Transport) remember(p *posted) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked()
	t.posted[p.requestID] = p
	t.byMessage[p.messageID] = p.requestID
}

func (t *Transport) pruneLocked() {
	now := t.now()
	for id, p := range t.posted {
		if p.expiresAt.IsZero() || now.Before(p.expiresAt) {
			continue
		}
		delete(t.posted, id)
		delete(t.byMessage, p.messageID)
	}
}

// forget drops a question from the correlation maps and returns it, or nil if
// it was already forgotten. This is what makes Settle idempotent.
func (t *Transport) forget(requestID string) *posted {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.posted[requestID]
	if !ok {
		return nil
	}
	delete(t.posted, requestID)
	delete(t.byMessage, p.messageID)
	return p
}

// sendAttachment uploads one file. The file is streamed, never buffered: an
// attachment may be the full 10 MiB the domain allows.
func (t *Transport) sendAttachment(ctx context.Context, att hitl.Attachment) error {
	f, err := os.Open(att.Path)
	if err != nil {
		return fmt.Errorf("open attachment: %w", err)
	}
	defer func() { _ = f.Close() }()

	name := att.Filename
	if name == "" {
		name = "attachment"
	}
	upload := &models.InputFileUpload{Filename: name, Data: f}
	caption := truncateUTF16(att.Caption, maxCaptionUnits)

	if att.Kind == hitl.KindImage {
		if _, err := t.api.SendPhoto(ctx, &bot.SendPhotoParams{
			ChatID:  t.chatID,
			Photo:   upload,
			Caption: caption,
		}); err != nil {
			return fmt.Errorf("send photo %s: %w", name, err)
		}
		return nil
	}

	if _, err := t.api.SendDocument(ctx, &bot.SendDocumentParams{
		ChatID:   t.chatID,
		Document: upload,
		Caption:  caption,
		// Without this Telegram sniffs the content and may turn a document
		// into a photo or a sticker, which loses the filename.
		DisableContentTypeDetection: true,
	}); err != nil {
		return fmt.Errorf("send document %s: %w", name, err)
	}
	return nil
}

// sendBody attaches the untruncated question body as Markdown.
func (t *Transport) sendBody(ctx context.Context, req *hitl.Request) error {
	upload := &models.InputFileUpload{
		Filename: bodyDocumentName(req.ID),
		Data:     strings.NewReader(req.Body),
	}
	if _, err := t.api.SendDocument(ctx, &bot.SendDocumentParams{
		ChatID:                      t.chatID,
		Document:                    upload,
		Caption:                     truncateUTF16("Full text: "+req.Title, maxCaptionUnits),
		DisableContentTypeDetection: true,
	}); err != nil {
		return fmt.Errorf("telegram: send body document: %w", err)
	}
	return nil
}

// Settle takes the buttons down and records the outcome on the question.
func (t *Transport) Settle(ctx context.Context, req *hitl.Request, ans *hitl.Answer) error {
	p := t.forget(req.ID)
	if p == nil {
		// Already settled, or this transport never posted the question.
		return nil
	}

	if _, err := t.api.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
		ChatID:    p.chatID,
		MessageID: p.messageID,
	}); err != nil && !isNotModified(err) {
		t.log.Warn("could not strip keyboard", "request_id", req.ID, "error", err)
	}

	if _, err := t.api.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:             p.chatID,
		MessageID:          p.messageID,
		Text:               p.text + "\n\n" + outcomeLine(ans),
		ParseMode:          models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: bot.True()},
	}); err != nil && !isNotModified(err) {
		return fmt.Errorf("telegram: settle %s: %w", req.ID, err)
	}
	return nil
}

// isNotModified reports the benign edit error Telegram returns when the new
// content matches the old, which a repeated settle produces.
func isNotModified(err error) bool {
	return err != nil && strings.Contains(err.Error(), "message is not modified")
}
