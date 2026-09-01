package discord

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"github.com/huketo/herdr-hitl/internal/hitl"
)

// Discord API v10 limits that shape every message this transport builds.
// Exceeding any of them is a 400 from the REST API, which the human never
// sees, so each one is enforced before the request leaves the process.
const (
	// maxContentRunes is the message content limit. Overflow is attached as a
	// file rather than split across two messages: two messages would carry
	// two sets of buttons for one question.
	maxContentRunes = 2000
	// maxCustomIDLen bounds a component custom_id.
	maxCustomIDLen = 100
	// maxButtonsPerRow and maxActionRows bound the component layout.
	maxButtonsPerRow = 5
	maxActionRows    = 5
	// selectMenuThreshold is the choice count above which buttons stop
	// fitting: 21 buttons already need five rows, leaving no room for the
	// free-text button, so a single select menu takes over.
	selectMenuThreshold = 20
	// maxSelectOptions bounds a string select menu.
	maxSelectOptions = 25
	// maxModalTitleLen bounds a modal title.
	maxModalTitleLen = 45
	// maxTextInputLen bounds a paragraph text input.
	maxTextInputLen = 4000
	// maxFileBytes is Discord's per-file ceiling for an unboosted upload.
	maxFileBytes = int64(hitl.MaxAttachmentBytes)
	// maxRequestBytes is the combined upload ceiling for one message.
	maxRequestBytes = int64(25 << 20)
	// shortIDLen is how much of a request id is shown to the human. Six hex
	// characters are enough to tell two concurrent questions apart.
	shortIDLen = 6
	// answerPreviewRunes bounds the answer echoed back into a settled message.
	answerPreviewRunes = 200
)

// freeTextLabel is the button that opens the free-text modal.
const freeTextLabel = "Write answer…"

// customIDPrefix namespaces every component this transport owns, so a channel
// shared with other bots never hands us an interaction we cannot decode.
const customIDPrefix = "hitl"

// Component kinds encoded into a custom_id.
const (
	kindChoice = "c"
	kindFree   = "free"
	kindModal  = "modal"
	kindText   = "text"
	kindSelect = "sel"
)

// customID is a decoded component identifier.
type customID struct {
	// RequestID is the question the component belongs to.
	RequestID string
	// Kind is one of the kind constants.
	Kind string
	// Index is the choice position, valid only when Kind is kindChoice.
	Index int
}

// encodeChoiceID builds the custom_id of the button (or select option) for the
// choice at idx. The index rather than the choice id keeps the payload short
// and stable even when a caller uses long choice ids.
func encodeChoiceID(requestID string, idx int) string {
	return customIDPrefix + ":" + requestID + ":" + kindChoice + ":" + strconv.Itoa(idx)
}

// encodeKindID builds the custom_id of a component that needs no index.
func encodeKindID(requestID, kind string) string {
	return customIDPrefix + ":" + requestID + ":" + kind
}

// parseCustomID decodes a custom_id (or select option value) produced by this
// package. It reports false for anything else, including components posted by
// other bots.
func parseCustomID(s string) (customID, bool) {
	parts := strings.Split(s, ":")
	if len(parts) < 3 || parts[0] != customIDPrefix || parts[1] == "" {
		return customID{}, false
	}
	id := customID{RequestID: parts[1], Kind: parts[2], Index: -1}
	switch id.Kind {
	case kindFree, kindModal, kindText, kindSelect:
		if len(parts) != 3 {
			return customID{}, false
		}
	case kindChoice:
		if len(parts) != 4 {
			return customID{}, false
		}
		idx, err := strconv.Atoi(parts[3])
		if err != nil || idx < 0 {
			return customID{}, false
		}
		id.Index = idx
	default:
		return customID{}, false
	}
	return id, true
}

// checkCustomID guards the 100-character limit. Request ids are 12 hex
// characters, so this only fires for a hand-built id, but a 400 from Discord
// would surface as "the question never appeared".
func checkCustomID(id string) error {
	if n := utf8.RuneCountInString(id); n > maxCustomIDLen {
		return fmt.Errorf("discord: custom_id %q is %d characters, above the %d character limit",
			id, n, maxCustomIDLen)
	}
	return nil
}

// buttonStyle maps a domain style hint onto a Discord button style.
//
// Link (style 5) and premium (style 6) buttons are deliberately unreachable:
// they fire no interaction, so a human pressing one would never be heard.
func buttonStyle(style string) discordgo.ButtonStyle {
	switch style {
	case hitl.StylePrimary:
		return discordgo.SuccessButton
	case hitl.StyleDanger:
		return discordgo.DangerButton
	default:
		return discordgo.SecondaryButton
	}
}

// buildComponents renders the answer controls for req: buttons chunked into
// action rows, or one select menu when there are too many choices for buttons,
// plus the free-text button when free text is allowed.
func buildComponents(req *hitl.Request) ([]discordgo.MessageComponent, error) {
	var rows [][]discordgo.MessageComponent

	switch {
	case len(req.Choices) > selectMenuThreshold:
		menu, err := choiceSelect(req)
		if err != nil {
			return nil, err
		}
		rows = append(rows, []discordgo.MessageComponent{menu})
	case len(req.Choices) > 0:
		row := make([]discordgo.MessageComponent, 0, maxButtonsPerRow)
		for i, c := range req.Choices {
			btn, err := choiceButton(req.ID, i, c)
			if err != nil {
				return nil, err
			}
			row = append(row, btn)
			if len(row) == maxButtonsPerRow {
				rows = append(rows, row)
				row = make([]discordgo.MessageComponent, 0, maxButtonsPerRow)
			}
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
	}

	if req.AllowFreeText {
		id := encodeKindID(req.ID, kindFree)
		if err := checkCustomID(id); err != nil {
			return nil, err
		}
		btn := discordgo.Button{
			Label:    freeTextLabel,
			Style:    discordgo.SecondaryButton,
			CustomID: id,
		}
		// A select menu must be alone in its row and a full button row cannot
		// take a sixth button, so both cases open a fresh row.
		if n := len(rows); n > 0 && len(rows[n-1]) < maxButtonsPerRow && isButtonRow(rows[n-1]) {
			rows[n-1] = append(rows[n-1], btn)
		} else {
			rows = append(rows, []discordgo.MessageComponent{btn})
		}
	}

	if len(rows) > maxActionRows {
		return nil, fmt.Errorf("discord: %d action rows exceeds the limit of %d", len(rows), maxActionRows)
	}

	out := make([]discordgo.MessageComponent, 0, len(rows))
	for _, row := range rows {
		out = append(out, discordgo.ActionsRow{Components: row})
	}
	return out, nil
}

// isButtonRow reports whether a row holds buttons and can therefore take one
// more.
func isButtonRow(row []discordgo.MessageComponent) bool {
	if len(row) == 0 {
		return false
	}
	_, ok := row[0].(discordgo.Button)
	return ok
}

// choiceButton builds the button for one choice.
func choiceButton(requestID string, idx int, c hitl.Choice) (discordgo.Button, error) {
	id := encodeChoiceID(requestID, idx)
	if err := checkCustomID(id); err != nil {
		return discordgo.Button{}, err
	}
	return discordgo.Button{
		Label:    c.Label,
		Style:    buttonStyle(c.Style),
		CustomID: id,
	}, nil
}

// choiceSelect builds a single-pick string select over every choice. Option
// values reuse the button encoding so one decoder covers both layouts.
func choiceSelect(req *hitl.Request) (discordgo.SelectMenu, error) {
	if len(req.Choices) > maxSelectOptions {
		return discordgo.SelectMenu{}, fmt.Errorf("discord: %d choices exceeds the %d select options limit",
			len(req.Choices), maxSelectOptions)
	}
	id := encodeKindID(req.ID, kindSelect)
	if err := checkCustomID(id); err != nil {
		return discordgo.SelectMenu{}, err
	}
	options := make([]discordgo.SelectMenuOption, 0, len(req.Choices))
	for i, c := range req.Choices {
		value := encodeChoiceID(req.ID, i)
		if err := checkCustomID(value); err != nil {
			return discordgo.SelectMenu{}, err
		}
		options = append(options, discordgo.SelectMenuOption{
			Label: c.Label,
			Value: value,
		})
	}
	one := 1
	return discordgo.SelectMenu{
		MenuType:    discordgo.StringSelectMenu,
		CustomID:    id,
		Placeholder: "Choose an answer",
		MinValues:   &one,
		MaxValues:   1,
		Options:     options,
	}, nil
}

// shortID trims a request id down to the prefix shown in message headers.
func shortID(id string) string {
	if len(id) > shortIDLen {
		return id[:shortIDLen]
	}
	return id
}

// overflowName is the filename used when the body does not fit in a message.
func overflowName(req *hitl.Request) string {
	return "question-" + shortID(req.ID) + ".md"
}

// header renders the title line plus the identification line that lets a human
// tell two concurrent agents apart.
func header(req *hitl.Request) string {
	var b strings.Builder
	if title := strings.TrimSpace(req.Title); title != "" {
		b.WriteString("**")
		b.WriteString(title)
		b.WriteString("**\n")
	}
	b.WriteString("`")
	b.WriteString(shortID(req.ID))
	b.WriteString("`")
	if label := req.Origin.Label(); label != "" {
		b.WriteString(" · ")
		b.WriteString(label)
	}
	return b.String()
}

// renderContent builds the message text for req. controls says whether the
// message will carry answer controls, which decides whether the reply hint is
// worth showing. The second return value reports that the body was truncated,
// in which case the caller attaches the full body as a Markdown file.
//
// Markdown bodies are inlined rather than only attached because Discord never
// renders an attached .md or .html file: an attachment-only question would
// force the human to download the question before reading it.
func renderContent(req *hitl.Request, controls bool) (string, bool) {
	head := header(req)
	hint := ""
	if controls && req.AllowFreeText {
		hint = "\n\n-# Reply to this message to answer in your own words."
	}
	body := strings.TrimSpace(req.Body)
	if body == "" {
		return clampRunes(head+hint, maxContentRunes), false
	}

	full := head + "\n\n" + body + hint
	if utf8.RuneCountInString(full) <= maxContentRunes {
		return full, false
	}

	marker := "\n\n-# … truncated; the full text is attached as " + overflowName(req)
	budget := maxContentRunes - utf8.RuneCountInString(head+"\n\n"+marker+hint)
	if budget < 1 {
		return clampRunes(head+hint, maxContentRunes), true
	}
	return head + "\n\n" + clampRunes(body, budget) + marker + hint, true
}

// settledContent rewrites a delivered question to show its outcome. The
// outcome line is never the part that gets clipped.
func settledContent(req *hitl.Request, ans *hitl.Answer) string {
	line := "\n\n" + outcomeLine(ans)
	base, _ := renderContent(req, false)
	room := maxContentRunes - utf8.RuneCountInString(line)
	if room < 0 {
		room = 0
	}
	return clampRunes(base, room) + line
}

// outcomeLine describes how a question ended.
func outcomeLine(ans *hitl.Answer) string {
	switch {
	case ans == nil:
		return "**Closed.**"
	case ans.Status == hitl.StatusAnswered:
		text := strings.TrimSpace(ans.Text)
		if ans.ChoiceLabel != "" {
			text = ans.ChoiceLabel
		}
		return fmt.Sprintf("**Answered:** %s — %s",
			clampRunes(oneLine(text), answerPreviewRunes), ans.Responder.Display())
	case ans.Status == hitl.StatusTimeout:
		return "**Timed out.** Nobody answered before the deadline."
	default:
		if reason := strings.TrimSpace(ans.Reason); reason != "" {
			return "**Canceled.** " + clampRunes(oneLine(reason), answerPreviewRunes)
		}
		return "**Canceled.**"
	}
}

// oneLine flattens text so it cannot break a single-line outcome.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// clampRunes truncates s to at most n runes, cutting on a rune boundary.
func clampRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// findTextInput walks a component tree for the value of a text input.
//
// The walk is recursive because a modal submission is not guaranteed to be a
// flat list of action rows: Discord nests components inside sections and
// containers, and the shape is decided by the API version, not by us. An empty
// customID matches the first text input found.
func findTextInput(components []discordgo.MessageComponent, customID string) (string, bool) {
	for _, comp := range components {
		switch c := comp.(type) {
		case *discordgo.TextInput:
			if v, ok := matchTextInput(*c, customID); ok {
				return v, true
			}
		case discordgo.TextInput:
			if v, ok := matchTextInput(c, customID); ok {
				return v, true
			}
		case *discordgo.ActionsRow:
			if v, ok := findTextInput(c.Components, customID); ok {
				return v, true
			}
		case discordgo.ActionsRow:
			if v, ok := findTextInput(c.Components, customID); ok {
				return v, true
			}
		case *discordgo.Container:
			if v, ok := findTextInput(c.Components, customID); ok {
				return v, true
			}
		case discordgo.Container:
			if v, ok := findTextInput(c.Components, customID); ok {
				return v, true
			}
		case *discordgo.Section:
			if v, ok := findTextInput(c.Components, customID); ok {
				return v, true
			}
		case discordgo.Section:
			if v, ok := findTextInput(c.Components, customID); ok {
				return v, true
			}
		}
	}
	return "", false
}

// matchTextInput reports the value of in when it is the input being looked for.
func matchTextInput(in discordgo.TextInput, customID string) (string, bool) {
	if customID != "" && in.CustomID != customID {
		return "", false
	}
	return in.Value, true
}

// postedMessage records where a question was delivered, so an answer typed as
// a plain message can be traced back to it.
type postedMessage struct {
	RequestID string
	ChannelID string
	MessageID string
}

// correlation is the outcome of matching a plain message to a question.
type correlation int

// Correlation outcomes.
const (
	// corrNone means the message answers nothing we posted.
	corrNone correlation = iota
	// corrMatched means exactly one question owns the message.
	corrMatched
	// corrAmbiguous means several questions could own it, so the human has to
	// disambiguate. Guessing would attach an answer to the wrong agent.
	corrAmbiguous
)

// correlate decides which question a plain message answers. replyTo is the
// referenced message id, empty when the human did not use the reply feature.
func correlate(posted []postedMessage, channelID, replyTo string) (string, correlation) {
	if replyTo != "" {
		for _, p := range posted {
			if p.MessageID == replyTo {
				return p.RequestID, corrMatched
			}
		}
		// A reply aimed at some other message is not an answer to us.
		return "", corrNone
	}

	var match string
	n := 0
	for _, p := range posted {
		if p.ChannelID != channelID {
			continue
		}
		n++
		match = p.RequestID
	}
	switch n {
	case 0:
		return "", corrNone
	case 1:
		return match, corrMatched
	default:
		return "", corrAmbiguous
	}
}

// checkUploadSize rejects an upload Discord would refuse. extra is the size of
// the generated body attachment, zero when the body fitted in the message.
//
// Failing here rather than at the API keeps the error actionable: a 413 from
// Discord arrives as an opaque REST failure long after the agent blocked.
func checkUploadSize(attachments []hitl.Attachment, extra int64) error {
	total := extra
	for _, att := range attachments {
		if att.Size > maxFileBytes {
			return fmt.Errorf("discord: attachment %s is %d bytes, above the %d byte per-file limit",
				att.Filename, att.Size, maxFileBytes)
		}
		total += att.Size
	}
	if total > maxRequestBytes {
		return fmt.Errorf("discord: attachments total %d bytes, above the %d byte per-message limit",
			total, maxRequestBytes)
	}
	return nil
}
