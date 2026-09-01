package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/huketo/herdr-hitl/internal/config"
	"github.com/huketo/herdr-hitl/internal/hitl"
)

// match classifies what a plain chat message can answer.
type match int

const (
	// matchNone means the message answers nothing we posted.
	matchNone match = iota
	// matchOne means exactly one outstanding question fits.
	matchOne
	// matchAmbiguous means several fit and the human must disambiguate.
	matchAmbiguous
)

// handleUpdate dispatches one Telegram update. The library runs each handler
// in its own goroutine, so this may run concurrently with itself; every read
// of the correlation maps takes the mutex.
func (t *Transport) handleUpdate(ctx context.Context, _ *bot.Bot, update *models.Update) {
	switch {
	case update.CallbackQuery != nil:
		t.handleCallback(ctx, update.CallbackQuery)
	case update.Message != nil:
		t.handleMessage(ctx, update.Message)
	}
}

// handleCallback turns a button press into an answer.
func (t *Transport) handleCallback(ctx context.Context, q *models.CallbackQuery) {
	requestID, index, ok := parseCallbackData(q.Data)
	if !ok {
		t.toast(ctx, q.ID, "Unrecognised button.")
		return
	}
	if !t.permitted(&q.From) {
		t.toast(ctx, q.ID, "You are not allowed to answer this question.")
		return
	}

	req, found := t.resolver.Lookup(requestID)
	if !found {
		t.toast(ctx, q.ID, "That question is no longer waiting for an answer.")
		return
	}
	if index >= len(req.Choices) {
		t.toast(ctx, q.ID, "That option is no longer offered.")
		return
	}
	choice := req.Choices[index]

	// Clients spin a progress indicator until the callback is answered, so
	// acknowledge before doing anything else.
	t.toast(ctx, q.ID, "")

	err := t.resolver.Resolve(&hitl.Answer{
		RequestID:   requestID,
		Status:      hitl.StatusAnswered,
		ChoiceID:    choice.ID,
		ChoiceLabel: choice.Label,
		Text:        choice.Label,
		Responder:   responderOf(&q.From),
	})
	switch {
	case err == nil:
		return
	case errors.Is(err, hitl.ErrAlreadyAnswered), errors.Is(err, hitl.ErrUnknownRequest):
		// Lost a race with another answer between Lookup and Resolve. The
		// first acknowledgement already stopped the spinner; this one is
		// best-effort and only adds the explanation.
		t.toast(ctx, q.ID, "That question was already answered.")
	default:
		t.log.Warn("resolve failed", "request_id", requestID, "error", err)
	}
}

// handleMessage turns a plain chat message into a free-text answer.
func (t *Transport) handleMessage(ctx context.Context, msg *models.Message) {
	if msg.From == nil || msg.From.IsBot {
		return
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	replyTo := 0
	if msg.ReplyToMessage != nil {
		replyTo = msg.ReplyToMessage.ID
	}

	requestID, result := t.matchFreeText(msg.Chat.ID, replyTo)
	switch result {
	case matchNone:
		// Ordinary chatter in a shared chat; staying quiet is the only
		// tolerable behaviour for a bot that lives in a group.
		return
	case matchAmbiguous:
		t.reply(ctx, msg, "Several questions are waiting. Reply directly to the one you want to answer, or tap one of its buttons.")
		return
	}

	if !t.permitted(msg.From) {
		t.reply(ctx, msg, "You are not allowed to answer this question.")
		return
	}

	err := t.resolver.Resolve(&hitl.Answer{
		RequestID: requestID,
		Status:    hitl.StatusAnswered,
		Text:      text,
		Responder: responderOf(msg.From),
	})
	switch {
	case err == nil:
		return
	case errors.Is(err, hitl.ErrAlreadyAnswered), errors.Is(err, hitl.ErrUnknownRequest):
		t.reply(ctx, msg, "That question is no longer waiting for an answer.")
	default:
		t.log.Warn("resolve failed", "request_id", requestID, "error", err)
	}
}

// matchFreeText finds the question a plain message answers.
//
// A reply is authoritative. Without one, a single outstanding question in that
// chat is unambiguous enough to bind to; several are not, and guessing would
// route an answer to the wrong agent.
func (t *Transport) matchFreeText(chatID int64, replyTo int) (string, match) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked()

	if replyTo != 0 {
		if id, ok := t.byMessage[replyTo]; ok {
			p := t.posted[id]
			if p != nil && p.chatID == chatID && p.allowFree {
				return id, matchOne
			}
			// Replying to a buttons-only question is not an answer.
			if p != nil {
				return "", matchNone
			}
		}
	}

	var candidate string
	n := 0
	for id, p := range t.posted {
		if p.chatID != chatID || !p.allowFree {
			continue
		}
		candidate = id
		n++
	}
	switch n {
	case 0:
		return "", matchNone
	case 1:
		return candidate, matchOne
	default:
		return "", matchAmbiguous
	}
}

// permitted enforces AllowedUserIDs. An empty list trusts everyone who can see
// the chat, which for a private chat is only its owner.
func (t *Transport) permitted(u *models.User) bool {
	if len(t.allowed) == 0 {
		return true
	}
	if u == nil {
		return false
	}
	if _, ok := t.allowed[strconv.FormatInt(u.ID, 10)]; ok {
		return true
	}
	// Handles are what a human actually knows about themselves, so accept
	// them too rather than making the numeric id the only workable value.
	if u.Username != "" {
		if _, ok := t.allowed[strings.ToLower(u.Username)]; ok {
			return true
		}
	}
	return false
}

// responderOf renders the human behind an update.
func responderOf(u *models.User) hitl.Responder {
	r := hitl.Responder{Transport: config.TransportTelegram}
	if u == nil {
		return r
	}
	r.UserID = strconv.FormatInt(u.ID, 10)
	switch {
	case u.Username != "":
		r.Username = "@" + u.Username
	default:
		r.Username = strings.TrimSpace(u.FirstName + " " + u.LastName)
	}
	return r
}

// toast acknowledges a callback query, optionally with a message. Telegram
// accepts one acknowledgement per query, so failures are logged, not retried.
func (t *Transport) toast(ctx context.Context, queryID, text string) {
	if _, err := t.api.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: queryID,
		Text:            truncateUTF16(text, maxToastUnits),
		ShowAlert:       text != "",
	}); err != nil {
		t.log.Debug("answer callback query failed", "error", err)
	}
}

// reply answers a chat message in place so the human sees which message the
// bot is talking about.
func (t *Transport) reply(ctx context.Context, msg *models.Message, text string) {
	if _, err := t.api.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: msg.Chat.ID,
		Text:   truncateUTF16(text, maxMessageUnits),
		ReplyParameters: &models.ReplyParameters{
			MessageID:                msg.ID,
			AllowSendingWithoutReply: true,
		},
	}); err != nil {
		t.log.Debug("reply failed", "error", err)
	}
}
