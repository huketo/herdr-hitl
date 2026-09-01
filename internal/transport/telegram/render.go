package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/go-telegram/bot/models"

	"github.com/huketo/herdr-hitl/internal/hitl"
)

// Telegram counts message lengths in UTF-16 code units, not bytes and not
// runes, so a single emoji outside the BMP costs two of every budget below.
const (
	// maxMessageUnits is the Bot API cap on sendMessage text.
	maxMessageUnits = 4096
	// maxCaptionUnits is the cap on a photo or document caption.
	maxCaptionUnits = 1024
	// maxToastUnits is the cap on answerCallbackQuery text.
	maxToastUnits = 200
	// settleReserve keeps room in the question message for the outcome line
	// Settle appends later, so settling can never overflow the message.
	settleReserve = 200
	// maxQuestionUnits is the budget Post renders a question into.
	maxQuestionUnits = maxMessageUnits - settleReserve
)

// Inline keyboard limits. callback_data is hard-capped at 64 bytes by the Bot
// API, which is why it only ever carries the request id and a choice index.
const (
	maxCallbackDataBytes = 64
	buttonsPerRow        = 3
	maxButtonsPerRow     = 8
	maxButtons           = 100
)

// truncateUTF16 returns the longest prefix of s costing at most limit UTF-16
// code units. It iterates by rune, so it never splits a surrogate pair.
func truncateUTF16(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	used := 0
	for i, r := range s {
		n := utf16.RuneLen(r)
		if n < 0 {
			// Invalid runes are transcoded to U+FFFD, one unit.
			n = 1
		}
		if used+n > limit {
			return s[:i]
		}
		used += n
	}
	return s
}

// utf16Len reports the cost of s in UTF-16 code units.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		c := utf16.RuneLen(r)
		if c < 0 {
			c = 1
		}
		n += c
	}
	return n
}

// htmlEscaper escapes the three characters Telegram's HTML parse mode treats
// as markup. Nothing else is escaped: numeric entities are not reliably
// rendered, and quotes are only special inside a tag, which interpolated text
// never is.
var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// escapeHTML makes s safe to interpolate into an HTML-parse-mode message.
func escapeHTML(s string) string { return htmlEscaper.Replace(s) }

// renderBody turns the agent's Markdown into safe HTML. Markdown is not
// rendered: inline syntax is left as literal text because a half-understood
// converter produces "can't parse entities" errors, which lose the question
// entirely. Only fenced code blocks are recognised, because monospaced diffs
// and snippets are the one thing that is unreadable without them.
func renderBody(md string) string {
	src := strings.ReplaceAll(md, "\r\n", "\n")
	lines := strings.Split(src, "\n")

	out := make([]string, 0, len(lines))
	var code []string
	inFence := false

	flush := func() {
		out = append(out, "<pre><code>"+escapeHTML(strings.Join(code, "\n"))+"</code></pre>")
		code = code[:0]
	}
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if inFence {
				flush()
			}
			inFence = !inFence
			continue
		}
		if inFence {
			code = append(code, line)
			continue
		}
		out = append(out, escapeHTML(line))
	}
	// An unterminated fence still has to close, or the message will not parse.
	if inFence {
		flush()
	}
	return strings.Join(out, "\n")
}

// question is a rendered question message.
type question struct {
	// Text is the HTML body of the message.
	Text string
	// Overflow is true when the body did not fit and the full text must be
	// attached as a document.
	Overflow bool
}

// composeQuestion renders req for Telegram's HTML parse mode. failed names the
// attachments that could not be uploaded, so the human is told what is missing
// rather than silently seeing an incomplete question.
func composeQuestion(req *hitl.Request, failed []string) question {
	header := questionHeader(req)
	footer := questionFooter(req, failed)

	build := func(body, note string) string {
		parts := make([]string, 0, 4)
		parts = append(parts, header)
		if body != "" {
			parts = append(parts, body)
		}
		if note != "" {
			parts = append(parts, note)
		}
		if footer != "" {
			parts = append(parts, footer)
		}
		return strings.Join(parts, "\n\n")
	}

	full := build(renderBody(req.Body), "")
	if utf16Len(full) <= maxQuestionUnits {
		return question{Text: full}
	}

	const note = "<i>Message truncated. The full text is attached as a document.</i>"
	runes := []rune(req.Body)
	candidate := func(n int) string {
		return build(renderBody(string(runes[:n]))+"\n…", note)
	}
	if utf16Len(candidate(0)) > maxQuestionUnits {
		// Only reachable with a pathological title; fall back to plain text
		// so a hard cut cannot land inside a tag.
		return question{Text: truncateUTF16(escapeHTML(req.Title), maxQuestionUnits), Overflow: true}
	}
	// Escaping and code fences change length non-linearly, so search for the
	// longest body prefix whose rendered form fits instead of guessing.
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if utf16Len(candidate(mid)) <= maxQuestionUnits {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return question{Text: candidate(lo), Overflow: true}
}

// questionHeader identifies the request and the agent that asked it, so two
// agents sharing one chat stay distinguishable.
func questionHeader(req *hitl.Request) string {
	var b strings.Builder
	b.WriteString("<b>")
	b.WriteString(escapeHTML(truncateUTF16(req.Title, hitl.MaxTitleLen)))
	b.WriteString("</b>\n<code>")
	b.WriteString(escapeHTML(req.ID))
	b.WriteString("</code>")
	if label := req.Origin.Label(); label != "" {
		b.WriteString(" · <i>")
		b.WriteString(escapeHTML(truncateUTF16(label, 120)))
		b.WriteString("</i>")
	}
	return b.String()
}

// questionFooter tells the human how to answer and what failed to upload.
func questionFooter(req *hitl.Request, failed []string) string {
	var lines []string
	switch {
	case req.AllowFreeText && len(req.Choices) > 0:
		lines = append(lines, "<i>Tap a button, or reply to this message with your answer.</i>")
	case req.AllowFreeText:
		lines = append(lines, "<i>Reply to this message with your answer.</i>")
	}
	if len(failed) > 0 {
		names := make([]string, 0, len(failed))
		for _, f := range failed {
			names = append(names, escapeHTML(truncateUTF16(f, 80)))
		}
		lines = append(lines, "<i>Could not upload: "+strings.Join(names, ", ")+"</i>")
	}
	return strings.Join(lines, "\n")
}

// outcomeLine renders the settled state appended to the question message.
func outcomeLine(ans *hitl.Answer) string {
	if ans == nil {
		return "<i>Closed.</i>"
	}
	switch ans.Status {
	case hitl.StatusAnswered:
		who := escapeHTML(truncateUTF16(ans.Responder.Display(), 64))
		what := ans.Text
		if ans.ChoiceLabel != "" {
			what = ans.ChoiceLabel
		}
		return "<b>Answered</b> by " + who + ": " + escapeHTML(truncateUTF16(what, 80))
	case hitl.StatusTimeout:
		return "<b>Timed out</b> — " + escapeHTML(truncateUTF16(reasonOr(ans, "no answer in time"), 80))
	default:
		return "<b>Canceled</b> — " + escapeHTML(truncateUTF16(reasonOr(ans, "withdrawn"), 80))
	}
}

func reasonOr(ans *hitl.Answer, fallback string) string {
	if strings.TrimSpace(ans.Reason) == "" {
		return fallback
	}
	return ans.Reason
}

// callbackData builds the payload carried by a choice button. It never holds
// the label or the body: the Bot API rejects callback_data over 64 bytes.
func callbackData(requestID string, index int) (string, bool) {
	data := requestID + ":" + strconv.Itoa(index)
	if len(data) > maxCallbackDataBytes {
		return "", false
	}
	return data, true
}

// parseCallbackData splits a payload back into request id and choice index.
// The split is on the last colon so a request id containing one still works.
func parseCallbackData(data string) (requestID string, index int, ok bool) {
	i := strings.LastIndexByte(data, ':')
	if i <= 0 || i == len(data)-1 {
		return "", 0, false
	}
	n, err := strconv.Atoi(data[i+1:])
	if err != nil || n < 0 {
		return "", 0, false
	}
	return data[:i], n, true
}

// buttonStyle maps a domain style hint onto the Bot API button style. Unknown
// hints render as the default button rather than failing the send.
func buttonStyle(style string) string {
	switch style {
	case hitl.StylePrimary:
		return "success"
	case hitl.StyleDanger:
		return "danger"
	default:
		return ""
	}
}

// replyPlaceholder is shown in the client's input box; the Bot API caps it at
// 64 characters.
const replyPlaceholder = "Type your answer"

// keyboard builds the inline keyboard for req, or nil when there is nothing to
// click.
//
// When free text is allowed the markup also sets force_reply, which is what
// makes the client pre-fill the reply target. Without it an incoming message
// carries no reply_to_message and can only be matched by the single-pending
// fallback, so two concurrent questions would both become unanswerable by
// text. Bot API 10.3 allows force_reply on an inline keyboard, so a question
// can offer buttons and a text box at the same time.
func keyboard(req *hitl.Request) models.ReplyMarkup {
	rows := choiceRows(req.ID, req.Choices, buttonsPerRow)
	if len(rows) == 0 {
		if !req.AllowFreeText {
			return nil
		}
		return models.ForceReply{
			ForceReply:            true,
			InputFieldPlaceholder: replyPlaceholder,
			Selective:             true,
		}
	}
	return models.InlineKeyboardMarkup{
		InlineKeyboard: rows,
		ForceReply:     req.AllowFreeText,
	}
}

// choiceRows chunks choices into keyboard rows, honouring the Bot API caps of
// eight buttons per row and one hundred buttons per keyboard.
func choiceRows(requestID string, choices []hitl.Choice, perRow int) [][]models.InlineKeyboardButton {
	if perRow < 1 {
		perRow = 1
	}
	if perRow > maxButtonsPerRow {
		perRow = maxButtonsPerRow
	}

	var rows [][]models.InlineKeyboardButton
	var row []models.InlineKeyboardButton
	total := 0
	for i, c := range choices {
		if total >= maxButtons {
			break
		}
		data, ok := callbackData(requestID, i)
		if !ok {
			continue
		}
		row = append(row, models.InlineKeyboardButton{
			Text:         truncateUTF16(c.Label, hitl.MaxChoiceLabelLen),
			CallbackData: data,
			Style:        buttonStyle(c.Style),
		})
		total++
		if len(row) == perRow {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return rows
}

// bodyDocumentName names the attachment carrying an overlong body.
func bodyDocumentName(requestID string) string {
	return fmt.Sprintf("question-%s.md", requestID)
}
