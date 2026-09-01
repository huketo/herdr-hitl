// Package discord delivers questions to a Discord channel or DM and receives
// answers over the gateway.
//
// One process owns one gateway connection for its whole lifetime. That is a
// hard requirement rather than an optimisation: Discord rate-limits IDENTIFY
// to 1000 per 24 hours and resets the bot token when the limit is exceeded, so
// a connection per question would eventually lock the user out of their own
// bot.
package discord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/huketo/herdr-hitl/internal/config"
	"github.com/huketo/herdr-hitl/internal/hitl"
	"github.com/huketo/herdr-hitl/internal/transport"
)

// Compile-time proof that this backend satisfies the transport contract.
var _ transport.Transport = (*Transport)(nil)

// readyTimeout bounds how long Start waits for the gateway READY event. A
// rejected IDENTIFY normally shows up as a disconnect well before this.
const readyTimeout = 30 * time.Second

// Transport is the Discord gateway transport.
type Transport struct {
	cfg      config.Discord
	resolver hitl.Resolver
	log      *slog.Logger
	session  *discordgo.Session
	intents  discordgo.Intent
	allowed  map[string]struct{}

	mu        sync.Mutex
	channelID string
	self      *discordgo.User
	opened    bool
	closed    bool
	posted    map[string]postedMessage
}

// New builds a Discord transport. It validates the configuration and registers
// the gateway handlers but opens no connection; call Start for that.
func New(cfg config.Discord, resolver hitl.Resolver, log *slog.Logger) (*Transport, error) {
	if strings.TrimSpace(cfg.BotToken) == "" {
		return nil, errors.New("discord: bot_token is required")
	}
	if strings.TrimSpace(cfg.ChannelID) == "" && strings.TrimSpace(cfg.UserID) == "" {
		return nil, errors.New("discord: set channel_id or user_id")
	}
	if resolver == nil {
		return nil, errors.New("discord: resolver is required")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	session, err := discordgo.New("Bot " + strings.TrimSpace(cfg.BotToken))
	if err != nil {
		return nil, fmt.Errorf("discord: create session: %w", err)
	}

	// discordgo's constructor asks for IntentsAllWithoutPrivileged, which is
	// both more than this transport reads and enough to fail the handshake on
	// a bot that was not granted it. Button, select, and modal interactions
	// are not intent-gated at all; only the plain-message free-text fallback
	// needs an intent, and message content in a DM with the app is delivered
	// without the privileged one.
	intents := discordgo.IntentDirectMessages | discordgo.IntentGuildMessages
	if cfg.MessageContentIntent {
		intents |= discordgo.IntentMessageContent
	}
	session.Identify.Intents = intents

	// Reconnecting is disabled until READY proves the IDENTIFY was accepted.
	// discordgo's reconnect loop treats a fresh socket as success, so a
	// gateway that closes with 4013/4014 every time would spin through the
	// IDENTIFY budget instead of reporting the misconfiguration.
	session.ShouldReconnectOnError = false

	allowed := make(map[string]struct{}, len(cfg.AllowedUserIDs))
	for _, id := range cfg.AllowedUserIDs {
		if id = strings.TrimSpace(id); id != "" {
			allowed[id] = struct{}{}
		}
	}

	t := &Transport{
		cfg:      cfg,
		resolver: resolver,
		log:      log,
		session:  session,
		intents:  intents,
		allowed:  allowed,
		posted:   make(map[string]postedMessage),
	}
	session.AddHandler(t.onInteraction)
	session.AddHandler(t.onMessage)
	return t, nil
}

// Name identifies the transport in configuration and CLI flags.
func (t *Transport) Name() string { return config.TransportDiscord }

// Start opens the gateway connection and returns once READY has been observed.
func (t *Transport) Start(ctx context.Context) error {
	ready := make(chan *discordgo.Ready, 1)
	removeReady := t.session.AddHandlerOnce(func(_ *discordgo.Session, r *discordgo.Ready) {
		select {
		case ready <- r:
		default:
		}
	})
	defer removeReady()

	// A disconnect before READY means the gateway rejected the handshake.
	dropped := make(chan struct{}, 1)
	removeDropped := t.session.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) {
		select {
		case dropped <- struct{}{}:
		default:
		}
	})
	defer removeDropped()

	if err := t.session.Open(); err != nil {
		return fmt.Errorf("discord: open gateway: %w", err)
	}
	t.mu.Lock()
	t.opened = true
	t.mu.Unlock()

	select {
	case r := <-ready:
		t.mu.Lock()
		t.self = r.User
		t.mu.Unlock()
	case <-dropped:
		t.closeQuietly()
		return t.identifyError()
	case <-ctx.Done():
		t.closeQuietly()
		return fmt.Errorf("discord: waiting for gateway READY: %w", ctx.Err())
	case <-time.After(readyTimeout):
		t.closeQuietly()
		return fmt.Errorf("discord: gateway sent no READY within %s", readyTimeout)
	}

	// The IDENTIFY was accepted, so transient drops are worth retrying.
	t.session.Lock()
	t.session.ShouldReconnectOnError = true
	t.session.Unlock()

	// Resolving the destination now turns a bad channel_id or an unreachable
	// user into a startup failure instead of a failed ask.
	if _, err := t.channel(ctx); err != nil {
		t.closeQuietly()
		return err
	}

	t.log.Info("discord transport ready", "target", t.Describe(), "intents", uint(t.intents))
	return nil
}

// identifyError explains a handshake the gateway refused.
func (t *Transport) identifyError() error {
	hint := ""
	if t.cfg.MessageContentIntent {
		hint = " message_content_intent is enabled here, so MESSAGE CONTENT INTENT must be toggled on in the portal."
	}
	return fmt.Errorf("discord: the gateway closed the connection before READY (requested intents %d): "+
		"the bot token may be invalid, or the requested gateway intents are not granted "+
		"(close codes 4013 and 4014). Check Bot > Privileged Gateway Intents in the Discord developer portal.%s",
		uint(t.intents), hint)
}

// Close tears the gateway connection down. It is idempotent and safe before
// Start.
func (t *Transport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	opened := t.opened
	t.opened = false
	t.mu.Unlock()

	if !opened {
		return nil
	}
	if err := t.session.Close(); err != nil {
		return fmt.Errorf("discord: close gateway: %w", err)
	}
	return nil
}

// closeQuietly tears down a half-started connection on an error path, where
// the close failure is never the interesting one.
func (t *Transport) closeQuietly() {
	if err := t.Close(); err != nil {
		t.log.Debug("discord: close after failed start", "error", err)
	}
}

// Discord error codes that mean "this bot may not message this person".
const (
	errCodeCannotSendToUser  = 50007
	errCodeNoMutualGuilds    = 50278
	invitePermissionBitfield = 125952
)

// explainSendError turns Discord's two "cannot DM" codes into the action that
// fixes them.
//
// A bot may only DM a user it shares a guild with, and UserChannelCreate
// succeeds anyway — the DM channel exists, it just cannot be written to. So
// the failure lands on the first question, long after setup, as a bare 403.
// The fix always looks the same and the bot knows its own application id, so
// the error carries the invite URL rather than describing it.
func (t *Transport) explainSendError(err error) error {
	var rest *discordgo.RESTError
	if !errors.As(err, &rest) || rest.Message == nil {
		return err
	}
	switch rest.Message.Code {
	case errCodeNoMutualGuilds, errCodeCannotSendToUser:
	default:
		return err
	}

	t.mu.Lock()
	self := t.self
	t.mu.Unlock()
	if self == nil {
		return fmt.Errorf("%w; a bot can only DM a user it shares a server with", err)
	}
	return fmt.Errorf(
		"%w; a bot can only DM a user it shares a server with. Invite it to one you are in: "+
			"https://discord.com/oauth2/authorize?client_id=%s&scope=bot%%20applications.commands&permissions=%d",
		err, self.ID, invitePermissionBitfield)
}

// Describe returns a one-line summary for `herdr-hitl doctor`. It never
// includes the bot token.
func (t *Transport) Describe() string {
	t.mu.Lock()
	self := t.self
	channelID := t.channelID
	t.mu.Unlock()

	who := "(not connected)"
	if self != nil {
		who = botTag(self)
	}
	if channelID == "" {
		channelID = strings.TrimSpace(t.cfg.ChannelID)
	}
	if channelID != "" {
		return fmt.Sprintf("discord: %s -> channel %s", who, channelID)
	}
	return fmt.Sprintf("discord: %s -> dm user %s", who, strings.TrimSpace(t.cfg.UserID))
}

// channel resolves the destination channel, opening a DM on first use and
// caching the result. UserChannelCreate returns the existing DM when there is
// one, but it is still a REST round trip worth doing once.
func (t *Transport) channel(ctx context.Context) (string, error) {
	t.mu.Lock()
	cached := t.channelID
	t.mu.Unlock()
	if cached != "" {
		return cached, nil
	}

	id := strings.TrimSpace(t.cfg.ChannelID)
	if id == "" {
		user := strings.TrimSpace(t.cfg.UserID)
		ch, err := t.session.UserChannelCreate(user, discordgo.WithContext(ctx))
		if err != nil {
			return "", fmt.Errorf("discord: open a DM with user %s: %w", user, err)
		}
		id = ch.ID
	}

	t.mu.Lock()
	t.channelID = id
	t.mu.Unlock()
	return id, nil
}

// Post delivers req to the destination channel.
func (t *Transport) Post(ctx context.Context, req *hitl.Request) error {
	if req == nil {
		return errors.New("discord: nil request")
	}
	channelID, err := t.channel(ctx)
	if err != nil {
		return err
	}

	// A request registered with the broker is awaiting an answer; a
	// notification is not, and must not offer controls that resolve to
	// nothing. The broker registers before it posts, so this is decided by
	// the time we get here.
	_, awaits := t.resolver.Lookup(req.ID)

	content, truncated := renderContent(req, awaits)
	var overflow int64
	if truncated {
		overflow = int64(len(req.Body))
	}
	if err := checkUploadSize(req.Attachments, overflow); err != nil {
		return err
	}

	files, closeFiles, err := openFiles(req, truncated)
	if err != nil {
		return err
	}
	defer closeFiles()

	// Components stay on the v1 message shape on purpose: the
	// IS_COMPONENTS_V2 flag is irreversible per message and disables both
	// content and inline attachments, which is most of this message.
	send := &discordgo.MessageSend{Content: content, Files: files}
	if awaits {
		components, err := buildComponents(req)
		if err != nil {
			return err
		}
		send.Components = components
	}

	msg, err := t.session.ChannelMessageSendComplex(channelID, send, discordgo.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("discord: send message to channel %s: %w", channelID, t.explainSendError(err))
	}

	if awaits {
		t.mu.Lock()
		t.posted[req.ID] = postedMessage{
			RequestID: req.ID,
			ChannelID: channelID,
			MessageID: msg.ID,
		}
		t.mu.Unlock()
	}
	t.log.Debug("discord: question posted",
		"request_id", req.ID, "channel", channelID, "message", msg.ID, "truncated", truncated)
	return nil
}

// openFiles opens every attachment plus, when the body overflowed the content
// limit, the body itself as a Markdown file. The returned func closes them and
// must run after the send completes.
func openFiles(req *hitl.Request, includeBody bool) ([]*discordgo.File, func(), error) {
	files := make([]*discordgo.File, 0, len(req.Attachments)+1)
	opened := make([]*os.File, 0, len(req.Attachments))
	closeAll := func() {
		for _, f := range opened {
			_ = f.Close()
		}
	}

	if includeBody {
		files = append(files, &discordgo.File{
			Name:        overflowName(req),
			ContentType: "text/markdown",
			Reader:      strings.NewReader(req.Body),
		})
	}
	for _, att := range req.Attachments {
		f, err := os.Open(att.Path)
		if err != nil {
			closeAll()
			return nil, nil, fmt.Errorf("discord: open attachment %s: %w", att.Path, err)
		}
		opened = append(opened, f)
		files = append(files, &discordgo.File{
			Name:        att.Filename,
			ContentType: att.MediaType,
			Reader:      f,
		})
	}
	return files, closeAll, nil
}

// Settle strips the controls from the delivered message and appends the
// outcome. It runs once per request: the record is dropped first, so a repeat
// call is a no-op.
func (t *Transport) Settle(ctx context.Context, req *hitl.Request, ans *hitl.Answer) error {
	if req == nil {
		return errors.New("discord: nil request")
	}
	t.mu.Lock()
	posted, ok := t.posted[req.ID]
	delete(t.posted, req.ID)
	t.mu.Unlock()
	if !ok {
		return nil
	}

	content := settledContent(req, ans)
	// A non-nil empty slice serialises as [] and clears the components; a nil
	// one would serialise as null and be rejected.
	empty := []discordgo.MessageComponent{}
	edit := discordgo.NewMessageEdit(posted.ChannelID, posted.MessageID)
	edit.Content = &content
	edit.Components = &empty

	if _, err := t.session.ChannelMessageEditComplex(edit, discordgo.WithContext(ctx)); err != nil {
		return fmt.Errorf("discord: edit message %s: %w", posted.MessageID, err)
	}
	return nil
}

// onInteraction dispatches a gateway interaction.
//
// The type is checked before any data accessor runs: discordgo's
// MessageComponentData and ModalSubmitData panic when the interaction is not
// the type they expect, and a panic in a handler goroutine would take the
// daemon down.
func (t *Transport) onInteraction(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	switch ic.Type {
	case discordgo.InteractionMessageComponent:
		t.onComponent(s, ic)
	case discordgo.InteractionModalSubmit:
		t.onModalSubmit(s, ic)
	default:
		// This transport registers no commands, so nothing else is ours.
	}
}

// onComponent handles a button press or a select choice. Every path responds,
// because Discord invalidates the interaction token after three seconds.
func (t *Transport) onComponent(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	data := ic.MessageComponentData()
	raw := data.CustomID
	// A select menu carries the pick in its values, not in its custom_id.
	if len(data.Values) > 0 {
		raw = data.Values[0]
	}
	id, ok := parseCustomID(raw)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), discordgo.InteractionDeadline)
	defer cancel()

	user := interactionUser(ic.Interaction)
	if !t.permitted(user) {
		t.respond(ctx, s, ic.Interaction, "You are not on this bot's allowed user list.",
			discordgo.MessageFlagsEphemeral)
		return
	}

	switch id.Kind {
	case kindChoice:
		t.answerChoice(ctx, s, ic.Interaction, id, user)
	case kindFree:
		t.openModal(ctx, s, ic.Interaction, id)
	default:
		// kindSelect with no values, or a component we no longer emit.
	}
}

// answerChoice records a predefined answer.
func (t *Transport) answerChoice(ctx context.Context, s *discordgo.Session,
	in *discordgo.Interaction, id customID, user *discordgo.User,
) {
	req, ok := t.resolver.Lookup(id.RequestID)
	if !ok {
		t.respond(ctx, s, in, "That question is no longer waiting for an answer.",
			discordgo.MessageFlagsEphemeral)
		return
	}
	if id.Index < 0 || id.Index >= len(req.Choices) {
		t.respond(ctx, s, in, "That option no longer exists.", discordgo.MessageFlagsEphemeral)
		return
	}
	choice := req.Choices[id.Index]
	ans := &hitl.Answer{
		RequestID:   req.ID,
		Status:      hitl.StatusAnswered,
		ChoiceID:    choice.ID,
		ChoiceLabel: choice.Label,
		Text:        choice.Label,
		Responder:   responder(user),
	}

	// Rewriting the message inside the interaction response is atomic: the
	// buttons disappear in the same operation that acknowledges the click, so
	// a double-click cannot land on a control that is still live.
	t.updateMessage(ctx, s, in, settledContent(req, ans))

	if err := t.resolver.Resolve(ans); err != nil {
		t.log.Warn("discord: resolve failed", "request_id", req.ID, "error", err)
	}
}

// openModal asks for a free-text answer.
func (t *Transport) openModal(ctx context.Context, s *discordgo.Session,
	in *discordgo.Interaction, id customID,
) {
	req, ok := t.resolver.Lookup(id.RequestID)
	if !ok {
		t.respond(ctx, s, in, "That question is no longer waiting for an answer.",
			discordgo.MessageFlagsEphemeral)
		return
	}
	title := clampRunes(strings.TrimSpace(req.Title), maxModalTitleLen)
	if title == "" {
		title = "Your answer"
	}

	resp := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: encodeKindID(req.ID, kindModal),
			Title:    title,
			// discordgo v0.29.0 predates the Label component (type 18), so
			// the text input has to sit in an action row. Discord still
			// accepts that shape for modals even though it is deprecated;
			// there is no alternative in this version.
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					discordgo.TextInput{
						CustomID:    encodeKindID(req.ID, kindText),
						Label:       "Answer",
						Style:       discordgo.TextInputParagraph,
						Placeholder: "Type your answer",
						Required:    true,
						MaxLength:   maxTextInputLen,
					},
				}},
			},
		},
	}
	if err := s.InteractionRespond(in, resp, discordgo.WithContext(ctx)); err != nil {
		t.log.Warn("discord: open modal failed", "request_id", req.ID, "error", err)
	}
}

// onModalSubmit records a free-text answer typed into the modal.
func (t *Transport) onModalSubmit(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	data := ic.ModalSubmitData()
	id, ok := parseCustomID(data.CustomID)
	if !ok || id.Kind != kindModal {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), discordgo.InteractionDeadline)
	defer cancel()

	user := interactionUser(ic.Interaction)
	if !t.permitted(user) {
		t.respond(ctx, s, ic.Interaction, "You are not on this bot's allowed user list.",
			discordgo.MessageFlagsEphemeral)
		return
	}

	text, found := findTextInput(data.Components, encodeKindID(id.RequestID, kindText))
	if !found {
		text, _ = findTextInput(data.Components, "")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		t.respond(ctx, s, ic.Interaction, "That answer was empty, so nothing was recorded.",
			discordgo.MessageFlagsEphemeral)
		return
	}

	req, ok := t.resolver.Lookup(id.RequestID)
	if !ok {
		t.respond(ctx, s, ic.Interaction, "That question is no longer waiting for an answer.",
			discordgo.MessageFlagsEphemeral)
		return
	}
	ans := &hitl.Answer{
		RequestID: req.ID,
		Status:    hitl.StatusAnswered,
		Text:      text,
		Responder: responder(user),
	}

	// A modal submission cannot open another modal. When Discord tells us
	// which message the modal came from we update it in place; otherwise the
	// broker's Settle call tidies the message up a moment later.
	if ic.Message != nil {
		t.updateMessage(ctx, s, ic.Interaction, settledContent(req, ans))
	} else {
		t.respond(ctx, s, ic.Interaction,
			"Answer recorded: "+clampRunes(oneLine(text), answerPreviewRunes), 0)
	}

	if err := t.resolver.Resolve(ans); err != nil {
		t.log.Warn("discord: resolve failed", "request_id", req.ID, "error", err)
	}
}

// updateMessage acknowledges an interaction by rewriting its message and
// removing every control.
func (t *Transport) updateMessage(ctx context.Context, s *discordgo.Session,
	in *discordgo.Interaction, content string,
) {
	resp := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			// Empty but non-nil: [] clears the controls, null is rejected.
			Components: []discordgo.MessageComponent{},
		},
	}
	if err := s.InteractionRespond(in, resp, discordgo.WithContext(ctx)); err != nil {
		t.log.Warn("discord: update message failed", "error", err)
	}
}

// respond sends a plain interaction reply. flags carries
// MessageFlagsEphemeral for messages only the presser should see.
func (t *Transport) respond(ctx context.Context, s *discordgo.Session,
	in *discordgo.Interaction, content string, flags discordgo.MessageFlags,
) {
	resp := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content, Flags: flags},
	}
	if err := s.InteractionRespond(in, resp, discordgo.WithContext(ctx)); err != nil {
		t.log.Warn("discord: interaction response failed", "error", err)
	}
}

// onMessage handles a free-text answer typed as an ordinary message, which is
// the fallback for humans who never press the button.
func (t *Transport) onMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author == nil || m.Author.Bot {
		return
	}
	t.mu.Lock()
	self := t.self
	channelID := t.channelID
	posted := make([]postedMessage, 0, len(t.posted))
	for _, p := range t.posted {
		posted = append(posted, p)
	}
	t.mu.Unlock()

	if self != nil && m.Author.ID == self.ID {
		return
	}
	if channelID == "" || m.ChannelID != channelID {
		return
	}
	// Without the Message Content intent Discord blanks content in guild
	// channels. The buttons and the modal still work, so there is nothing to
	// report here.
	text := strings.TrimSpace(m.Content)
	if text == "" {
		return
	}

	replyTo := ""
	if m.MessageReference != nil {
		replyTo = m.MessageReference.MessageID
	}
	reqID, result := correlate(posted, m.ChannelID, replyTo)

	switch result {
	case corrMatched:
		if !t.permitted(m.Author) {
			// A channel message has no ephemeral reply, so say it out loud.
			t.reply(s, m, "You are not on this bot's allowed user list.")
			return
		}
		ans := &hitl.Answer{
			RequestID: reqID,
			Status:    hitl.StatusAnswered,
			Text:      text,
			Responder: responder(m.Author),
		}
		if err := t.resolver.Resolve(ans); err != nil {
			t.log.Warn("discord: resolve failed", "request_id", reqID, "error", err)
			return
		}
		t.log.Debug("discord: answered by message", "request_id", reqID, "message", m.ID)
	case corrAmbiguous:
		if !t.permitted(m.Author) {
			return
		}
		t.reply(s, m, "Several questions are open. Reply directly to the one you mean, "+
			"or press one of its buttons.")
	case corrNone:
		// Ordinary chatter in a shared channel.
	}
}

// reply answers a human's message in the channel it arrived in.
func (t *Transport) reply(s *discordgo.Session, m *discordgo.MessageCreate, content string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	send := &discordgo.MessageSend{Content: content, Reference: m.SoftReference()}
	if _, err := s.ChannelMessageSendComplex(m.ChannelID, send, discordgo.WithContext(ctx)); err != nil {
		t.log.Warn("discord: reply failed", "channel", m.ChannelID, "error", err)
	}
}

// permitted reports whether a user may answer.
func (t *Transport) permitted(u *discordgo.User) bool {
	if len(t.allowed) == 0 {
		return true
	}
	if u == nil {
		return false
	}
	_, ok := t.allowed[u.ID]
	return ok
}

// interactionUser finds the human behind an interaction. Discord fills User in
// a DM and Member in a guild.
func interactionUser(in *discordgo.Interaction) *discordgo.User {
	switch {
	case in == nil:
		return nil
	case in.User != nil:
		return in.User
	case in.Member != nil:
		return in.Member.User
	default:
		return nil
	}
}

// responder records who answered.
func responder(u *discordgo.User) hitl.Responder {
	r := hitl.Responder{Transport: config.TransportDiscord}
	if u != nil {
		r.UserID = u.ID
		r.Username = displayName(u)
	}
	return r
}

// displayName renders a user the way Discord shows them.
func displayName(u *discordgo.User) string {
	switch {
	case u == nil:
		return ""
	case u.GlobalName != "":
		return u.GlobalName
	case u.Discriminator != "" && u.Discriminator != "0":
		return u.Username + "#" + u.Discriminator
	default:
		return u.Username
	}
}

// botTag renders this bot's own account as name#discriminator, which is how
// the developer portal shows it. A bot's GlobalName is the application name
// and drops the discriminator, so it is the wrong side of the identity for
// `doctor` output.
func botTag(u *discordgo.User) string {
	if u == nil {
		return ""
	}
	if u.Discriminator != "" && u.Discriminator != "0" {
		return u.Username + "#" + u.Discriminator
	}
	return displayName(u)
}
