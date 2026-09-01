package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/huketo/herdr-hitl/internal/config"
	"github.com/huketo/herdr-hitl/internal/hitl"
)

// apiCall is one request the transport made to the Bot API.
type apiCall struct {
	Method string
	Values map[string]string
	Files  map[string]string // form field -> filename
	// EmptyForm records a well-formed multipart envelope carrying no fields
	// at all, which is the shape Telegram rejects. A request with no body is
	// not an EmptyForm.
	EmptyForm bool
}

// fakeAPI is a stand-in Bot API server. It exists so the wire shape of every
// call — parse mode, keyboard payloads, upload ordering — is asserted against
// real HTTP instead of against a mock of our own design.
type fakeAPI struct {
	*httptest.Server

	// chatType is what getChat reports. Empty means "private", which is the
	// permissive case; set it to "channel" to exercise the chat kind that
	// accepts inline keyboards and nothing else.
	chatType string

	mu        sync.Mutex
	calls     []apiCall
	nextMsgID int
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{nextMsgID: 100}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]

	call := apiCall{Method: method, Values: map[string]string{}, Files: map[string]string{}}
	parsed := false
	if err := r.ParseMultipartForm(4 << 20); err == nil && r.MultipartForm != nil {
		parsed = true
		for k, v := range r.MultipartForm.Value {
			call.Values[k] = v[0]
		}
		for k, v := range r.MultipartForm.File {
			call.Files[k] = v[0].Filename
		}
	}

	// Telegram rejects a well-formed multipart envelope that carries no
	// fields: measured against api.telegram.org, it answers 400 with a
	// zero-length body, which decodes as "unexpected end of JSON input"
	// rather than as an API error. A parameterless call must therefore send
	// no body at all. Reproducing that here is what stops a params struct
	// whose fields are all unset from reaching production again.
	if parsed && len(call.Values) == 0 && len(call.Files) == 0 {
		call.EmptyForm = true
		f.mu.Lock()
		f.calls = append(f.calls, call)
		f.mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.nextMsgID++
	msgID := f.nextMsgID
	f.mu.Unlock()
	var result string
	switch method {
	case "getMe":
		result = `{"id":1,"is_bot":true,"username":"herdr_hitl_bot"}`
	case "deleteWebhook", "answerCallbackQuery":
		result = `true`
	case "getChat":
		kind := f.chatType
		if kind == "" {
			kind = "private"
		}
		result = fmt.Sprintf(`{"id":-100777,"type":%q,"title":"test"}`, kind)
	case "getUpdates":
		// Answer the long poll immediately but not instantly, so the loop
		// does not spin the test machine while a case waits on it.
		time.Sleep(10 * time.Millisecond)
		result = `[]`
	default:
		result = fmt.Sprintf(`{"message_id":%d,"date":1,"chat":{"id":-100777,"type":"group"}}`, msgID)
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"ok":true,"result":%s}`, result)
}

// waitFor polls cond until it holds, so tests never sleep on the poll loop.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (f *fakeAPI) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.Method)
	}
	return out
}

func (f *fakeAPI) last(method string) (apiCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Method == method {
			return f.calls[i], true
		}
	}
	return apiCall{}, false
}

func (f *fakeAPI) mustLast(t *testing.T, method string) apiCall {
	t.Helper()
	c, ok := f.last(method)
	if !ok {
		t.Fatalf("%s was never called; calls were %v", method, f.methods())
	}
	return c
}

func newWiredTransport(t *testing.T, api *fakeAPI, resolver hitl.Resolver, allowed ...string) *Transport {
	t.Helper()
	tr, err := New(config.Telegram{
		BotToken:       "111222:AAFakeTokenForTests",
		ChatID:         "-100777",
		APIBase:        api.URL,
		AllowedUserIDs: allowed,
	}, resolver, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

func TestPostSendsAttachmentsBeforeTheQuestion(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	png := filepath.Join(dir, "screenshot.png")
	if err := os.WriteFile(png, []byte("not really a png"), 0o600); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	att, err := hitl.NewAttachment(png)
	if err != nil {
		t.Fatalf("NewAttachment: %v", err)
	}
	att.Caption = "the failing screen"

	api := newFakeAPI(t)
	tr := newWiredTransport(t, api, newFakeResolver())

	req := &hitl.Request{
		ID:            "abc123def456",
		Title:         "Deploy <prod>?",
		Body:          "The diff touches `main`.",
		Choices:       []hitl.Choice{{ID: "ship", Label: "Ship", Style: hitl.StylePrimary}, {ID: "hold", Label: "Hold", Style: hitl.StyleDanger}},
		AllowFreeText: true,
		Attachments:   []hitl.Attachment{att},
	}
	if err := tr.Post(context.Background(), req); err != nil {
		t.Fatalf("Post: %v", err)
	}

	if got, want := api.methods(), []string{"sendPhoto", "sendMessage"}; !equalStrings(got, want) {
		t.Fatalf("call order = %v, want %v", got, want)
	}

	photo := api.mustLast(t, "sendPhoto")
	if photo.Files["photo"] != "screenshot.png" {
		t.Fatalf("photo upload filename = %q", photo.Files["photo"])
	}
	if photo.Values["caption"] != "the failing screen" {
		t.Fatalf("photo caption = %q", photo.Values["caption"])
	}

	msg := api.mustLast(t, "sendMessage")
	if msg.Values["parse_mode"] != string(models.ParseModeHTML) {
		t.Fatalf("parse_mode = %q, want HTML", msg.Values["parse_mode"])
	}
	if msg.Values["chat_id"] != "-100777" {
		t.Fatalf("chat_id = %q", msg.Values["chat_id"])
	}
	if !strings.Contains(msg.Values["text"], "Deploy &lt;prod&gt;?") {
		t.Fatalf("question text is not HTML escaped: %q", msg.Values["text"])
	}
	if !strings.Contains(msg.Values["text"], "abc123def456") {
		t.Fatalf("question text omits the request id: %q", msg.Values["text"])
	}

	var markup models.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(msg.Values["reply_markup"]), &markup); err != nil {
		t.Fatalf("reply_markup %q: %v", msg.Values["reply_markup"], err)
	}
	if len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 2 {
		t.Fatalf("unexpected keyboard shape: %+v", markup.InlineKeyboard)
	}
	if got, want := markup.InlineKeyboard[0][0].CallbackData, "abc123def456:0"; got != want {
		t.Fatalf("callback_data = %q, want %q", got, want)
	}
	if got := markup.InlineKeyboard[0][0].Style; got != "success" {
		t.Fatalf("primary button style = %q, want success", got)
	}
	if got := markup.InlineKeyboard[0][1].Style; got != "danger" {
		t.Fatalf("danger button style = %q", got)
	}
}

func TestPostAttachesAnOverlongBody(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	tr := newWiredTransport(t, api, newFakeResolver())

	req := &hitl.Request{
		ID:            "over01flow02",
		Title:         "Long question",
		Body:          strings.Repeat("a very long line of context\n", 400),
		AllowFreeText: true,
	}
	if err := tr.Post(context.Background(), req); err != nil {
		t.Fatalf("Post: %v", err)
	}

	if got, want := api.methods(), []string{"sendDocument", "sendMessage"}; !equalStrings(got, want) {
		t.Fatalf("call order = %v, want %v", got, want)
	}
	doc := api.mustLast(t, "sendDocument")
	if doc.Files["document"] != "question-over01flow02.md" {
		t.Fatalf("body document filename = %q", doc.Files["document"])
	}
	if doc.Values["disable_content_type_detection"] != "true" {
		t.Fatalf("documents must disable content type sniffing, got %q", doc.Values["disable_content_type_detection"])
	}
	msg := api.mustLast(t, "sendMessage")
	if !strings.Contains(msg.Values["text"], "attached as a document") {
		t.Fatalf("truncated question does not mention the attachment: %q", msg.Values["text"])
	}
}

func TestPostSurvivesAMissingAttachment(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	tr := newWiredTransport(t, api, newFakeResolver())

	req := &hitl.Request{
		ID:            "miss01ing002",
		Title:         "Question",
		Body:          "body",
		AllowFreeText: true,
		Attachments:   []hitl.Attachment{{Path: filepath.Join(t.TempDir(), "gone.png"), Filename: "gone.png", Kind: hitl.KindImage}},
	}
	// Losing an attachment must not cost the human the question itself.
	if err := tr.Post(context.Background(), req); err != nil {
		t.Fatalf("Post: %v", err)
	}
	msg := api.mustLast(t, "sendMessage")
	if !strings.Contains(msg.Values["text"], "Could not upload: gone.png") {
		t.Fatalf("question does not report the failed upload: %q", msg.Values["text"])
	}
}

func TestCallbackAnswersTheRequest(t *testing.T) {
	t.Parallel()

	req := &hitl.Request{
		ID:      "cb01cb02cb03",
		Title:   "Question",
		Body:    "body",
		Choices: []hitl.Choice{{ID: "ship", Label: "Ship"}, {ID: "hold", Label: "Hold"}},
	}
	resolver := newFakeResolver(req)
	api := newFakeAPI(t)
	tr := newWiredTransport(t, api, resolver)

	tr.handleCallback(context.Background(), &models.CallbackQuery{
		ID:   "q1",
		Data: "cb01cb02cb03:1",
		From: models.User{ID: 42, Username: "huke"},
	})

	ack := api.mustLast(t, "answerCallbackQuery")
	if ack.Values["callback_query_id"] != "q1" {
		t.Fatalf("acknowledged the wrong query: %+v", ack.Values)
	}
	if _, ok := ack.Values["text"]; ok {
		t.Fatalf("a successful press should not raise a toast: %+v", ack.Values)
	}

	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if len(resolver.answers) != 1 {
		t.Fatalf("got %d answers, want 1", len(resolver.answers))
	}
	ans := resolver.answers[0]
	if ans.ChoiceID != "hold" || ans.Text != "Hold" {
		t.Fatalf("resolved the wrong choice: %+v", ans)
	}
	if ans.Responder.UserID != "42" || ans.Responder.Username != "@huke" {
		t.Fatalf("responder = %+v", ans.Responder)
	}
}

func TestCallbackLosingTheRaceExplainsItself(t *testing.T) {
	t.Parallel()

	req := &hitl.Request{ID: "rc01rc02rc03", Choices: []hitl.Choice{{ID: "ship", Label: "Ship"}}}
	resolver := newFakeResolver(req)
	resolver.err = hitl.ErrAlreadyAnswered
	api := newFakeAPI(t)
	tr := newWiredTransport(t, api, resolver)

	// Through handleUpdate, so the dispatch itself is exercised too.
	tr.handleUpdate(context.Background(), nil, &models.Update{
		CallbackQuery: &models.CallbackQuery{
			ID:   "q1",
			Data: "rc01rc02rc03:0",
			From: models.User{ID: 42},
		},
	})

	// The spinner is stopped first, then the outcome is explained.
	if got := api.methods(); !equalStrings(got, []string{"answerCallbackQuery", "answerCallbackQuery"}) {
		t.Fatalf("calls = %v, want two acknowledgements", got)
	}
	ack := api.mustLast(t, "answerCallbackQuery")
	if !strings.Contains(ack.Values["text"], "already answered") {
		t.Fatalf("lost race was not explained: %+v", ack.Values)
	}
}

func TestCallbackRejections(t *testing.T) {
	t.Parallel()

	req := &hitl.Request{ID: "cb01cb02cb03", Choices: []hitl.Choice{{ID: "ship", Label: "Ship"}}}

	tests := []struct {
		name    string
		allowed []string
		query   *models.CallbackQuery
	}{
		{
			name:  "unparseable payload",
			query: &models.CallbackQuery{ID: "q", Data: "garbage", From: models.User{ID: 42}},
		},
		{
			name:  "unknown request",
			query: &models.CallbackQuery{ID: "q", Data: "nosuchrequest:0", From: models.User{ID: 42}},
		},
		{
			name:  "index out of range",
			query: &models.CallbackQuery{ID: "q", Data: "cb01cb02cb03:9", From: models.User{ID: 42}},
		},
		{
			name:    "user not allowed",
			allowed: []string{"7"},
			query:   &models.CallbackQuery{ID: "q", Data: "cb01cb02cb03:0", From: models.User{ID: 42}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resolver := newFakeResolver(req)
			api := newFakeAPI(t)
			tr := newWiredTransport(t, api, resolver, tc.allowed...)

			tr.handleCallback(context.Background(), tc.query)

			ack := api.mustLast(t, "answerCallbackQuery")
			if ack.Values["text"] == "" {
				t.Fatalf("a refused press must explain itself: %+v", ack.Values)
			}
			resolver.mu.Lock()
			defer resolver.mu.Unlock()
			if len(resolver.answers) != 0 {
				t.Fatalf("refused press still resolved: %+v", resolver.answers)
			}
		})
	}
}

func TestFreeTextReplyAnswersTheRequest(t *testing.T) {
	t.Parallel()

	req := &hitl.Request{ID: "ft01ft02ft03", Title: "Question", Body: "body", AllowFreeText: true}
	resolver := newFakeResolver(req)
	api := newFakeAPI(t)
	tr := newWiredTransport(t, api, resolver)

	if err := tr.Post(context.Background(), req); err != nil {
		t.Fatalf("Post: %v", err)
	}
	tr.mu.Lock()
	posted := tr.posted[req.ID]
	tr.mu.Unlock()
	if posted == nil {
		t.Fatal("Post did not track the question")
	}

	tr.handleMessage(context.Background(), &models.Message{
		ID:             500,
		Chat:           models.Chat{ID: posted.chatID},
		From:           &models.User{ID: 42, Username: "huke"},
		Text:           "  use the staging cluster  ",
		ReplyToMessage: &models.Message{ID: posted.messageID},
	})

	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if len(resolver.answers) != 1 {
		t.Fatalf("got %d answers, want 1", len(resolver.answers))
	}
	if got, want := resolver.answers[0].Text, "use the staging cluster"; got != want {
		t.Fatalf("answer text = %q, want %q", got, want)
	}
}

func TestAmbiguousFreeTextAsksForAReply(t *testing.T) {
	t.Parallel()

	resolver := newFakeResolver()
	api := newFakeAPI(t)
	tr := newWiredTransport(t, api, resolver)

	track(t, tr, "reqA", -100777, 10, true)
	track(t, tr, "reqB", -100777, 11, true)

	tr.handleMessage(context.Background(), &models.Message{
		ID:   500,
		Chat: models.Chat{ID: -100777},
		From: &models.User{ID: 42},
		Text: "yes",
	})

	msg := api.mustLast(t, "sendMessage")
	if !strings.Contains(msg.Values["text"], "Reply directly") {
		t.Fatalf("ambiguity was not explained: %q", msg.Values["text"])
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if len(resolver.answers) != 0 {
		t.Fatalf("ambiguous message was guessed at: %+v", resolver.answers)
	}
}

func TestUnrelatedChatterIsIgnored(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	tr := newWiredTransport(t, api, newFakeResolver())

	tr.handleMessage(context.Background(), &models.Message{
		ID:   500,
		Chat: models.Chat{ID: -100777},
		From: &models.User{ID: 42},
		Text: "morning all",
	})
	tr.handleMessage(context.Background(), &models.Message{
		ID:   501,
		Chat: models.Chat{ID: -100777},
		From: &models.User{ID: 1, IsBot: true},
		Text: "beep",
	})

	if calls := api.methods(); len(calls) != 0 {
		t.Fatalf("bot chattered back at an unrelated message: %v", calls)
	}
}

func TestSettleStripsControlsAndIsIdempotent(t *testing.T) {
	t.Parallel()

	req := &hitl.Request{
		ID:      "st01st02st03",
		Title:   "Question",
		Body:    "body",
		Choices: []hitl.Choice{{ID: "ship", Label: "Ship"}},
	}
	api := newFakeAPI(t)
	tr := newWiredTransport(t, api, newFakeResolver(req))

	if err := tr.Post(context.Background(), req); err != nil {
		t.Fatalf("Post: %v", err)
	}
	ans := &hitl.Answer{
		RequestID:   req.ID,
		Status:      hitl.StatusAnswered,
		ChoiceID:    "ship",
		ChoiceLabel: "Ship",
		Text:        "Ship",
		Responder:   hitl.Responder{Transport: config.TransportTelegram, Username: "@huke"},
	}
	if err := tr.Settle(context.Background(), req, ans); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	markup := api.mustLast(t, "editMessageReplyMarkup")
	if _, ok := markup.Values["reply_markup"]; ok {
		t.Fatalf("settling must omit reply_markup to remove the buttons: %+v", markup.Values)
	}
	edit := api.mustLast(t, "editMessageText")
	if !strings.Contains(edit.Values["text"], "<b>Answered</b> by @huke: Ship") {
		t.Fatalf("outcome missing from the settled message: %q", edit.Values["text"])
	}
	if edit.Values["parse_mode"] != string(models.ParseModeHTML) {
		t.Fatalf("settled message parse_mode = %q", edit.Values["parse_mode"])
	}

	before := len(api.methods())
	if err := tr.Settle(context.Background(), req, ans); err != nil {
		t.Fatalf("second Settle: %v", err)
	}
	if after := len(api.methods()); after != before {
		t.Fatalf("second Settle made %d extra calls", after-before)
	}

	// A press arriving after settling has nothing to bind to.
	tr.mu.Lock()
	_, stillTracked := tr.byMessage[0]
	tr.mu.Unlock()
	if stillTracked {
		t.Fatal("settled question is still tracked")
	}
}

func TestStartHandshakeAndClose(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	tr := newWiredTransport(t, api, newFakeResolver())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := tr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	methods := api.methods()
	if len(methods) < 2 || methods[0] != "deleteWebhook" || methods[1] != "getMe" {
		t.Fatalf("startup handshake = %v, want deleteWebhook then getMe", methods)
	}
	if got, want := tr.Describe(), "telegram: @herdr_hitl_bot -> chat -100777"; got != want {
		t.Fatalf("Describe = %q, want %q", got, want)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestGetUpdatesRequestsBothUpdateKinds(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	tr := newWiredTransport(t, api, newFakeResolver())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := tr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = tr.Close() }()

	// The Bot API reuses the previous allowed_updates when the parameter is
	// omitted, so it has to be on the wire every time.
	waitFor(t, func() bool {
		c, ok := api.last("getUpdates")
		return ok && strings.Contains(c.Values["allowed_updates"], "callback_query") &&
			strings.Contains(c.Values["allowed_updates"], "message")
	}, "getUpdates carrying both allowed update kinds")
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// calls returns a copy of every request the fake API received.
func (f *fakeAPI) calledWith() []apiCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]apiCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func TestStartSendsNoEmptyMultipartEnvelope(t *testing.T) {
	t.Parallel()

	// Regression: Start once called DeleteWebhook with &DeleteWebhookParams{}.
	// Every field on that struct is optional, so the client encoded a
	// multipart form with zero fields, and Telegram answered 400 with an
	// empty body. The daemon refused to start with the useless message
	// "unexpected end of JSON input". The fake API reproduces that rejection,
	// so this test fails if a parameterless call regains a params struct.
	api := newFakeAPI(t)
	tr := newWiredTransport(t, api, newFakeResolver())

	if err := tr.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	if _, ok := api.last("deleteWebhook"); !ok {
		t.Fatal("Start did not clear the webhook; getUpdates fails with 409 while one is set")
	}
	for _, call := range api.calledWith() {
		if call.EmptyForm {
			t.Fatalf("%s was sent as an empty multipart form; Telegram answers that with 400", call.Method)
		}
	}
}

func TestPostRefusesAFreeTextOnlyQuestionInAChannel(t *testing.T) {
	t.Parallel()

	// A channel has no reply affordance, so a question whose only answer is
	// typed can never be answered. Posting it anyway would put a dead message
	// in the chat and block the agent until its deadline; the operator learns
	// nothing until then. Refusing names the problem and the two fixes.
	api := newFakeAPI(t)
	api.chatType = "channel"
	tr := newWiredTransport(t, api, newFakeResolver())

	if err := tr.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	err := tr.Post(t.Context(), &hitl.Request{
		ID:            "abc123def456",
		Title:         "Which pooler?",
		Body:          "Pick one.",
		AllowFreeText: true,
	})
	if err == nil {
		t.Fatal("Post = nil, want a refusal naming the channel")
	}
	for _, want := range []string{"channel", "-c", "chat_id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if _, sent := api.last("sendMessage"); sent {
		t.Error("a refused question must not reach the chat")
	}
}

func TestPostInAChannelSendsAnInlineKeyboardOnly(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	api.chatType = "channel"
	tr := newWiredTransport(t, api, newFakeResolver())

	if err := tr.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	if err := tr.Post(t.Context(), &hitl.Request{
		ID:            "abc123def456",
		Title:         "Ship it?",
		Body:          "main is green.",
		Choices:       []hitl.Choice{{ID: "yes", Label: "Yes"}},
		AllowFreeText: true,
	}); err != nil {
		t.Fatalf("Post: %v", err)
	}

	call := api.mustLast(t, "sendMessage")
	markup := call.Values["reply_markup"]
	if !strings.Contains(markup, "inline_keyboard") {
		t.Fatalf("reply_markup = %q, want an inline keyboard", markup)
	}
	if strings.Contains(markup, "force_reply") {
		t.Errorf("reply_markup = %q; force_reply is rejected by a channel", markup)
	}
	if strings.Contains(call.Values["text"], "reply to this message") {
		t.Error("the question promises a reply box that a channel does not have")
	}
}
