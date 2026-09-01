package discord

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"github.com/huketo/herdr-hitl/internal/hitl"
)

// choices builds n distinct choices for layout tests.
func choices(n int) []hitl.Choice {
	out := make([]hitl.Choice, 0, n)
	for i := range n {
		out = append(out, hitl.Choice{ID: string(rune('a'+i%26)) + string(rune('0'+i/26)), Label: "opt"})
	}
	return out
}

func TestCustomIDRoundTrip(t *testing.T) {
	t.Parallel()

	requestID := hitl.NewID()

	t.Run("choice", func(t *testing.T) {
		t.Parallel()
		for idx := range hitl.MaxChoices {
			encoded := encodeChoiceID(requestID, idx)
			if n := utf8.RuneCountInString(encoded); n > maxCustomIDLen {
				t.Fatalf("custom_id %q is %d characters, above the %d limit", encoded, n, maxCustomIDLen)
			}
			got, ok := parseCustomID(encoded)
			if !ok {
				t.Fatalf("parseCustomID(%q) failed", encoded)
			}
			if got.RequestID != requestID || got.Kind != kindChoice || got.Index != idx {
				t.Fatalf("round trip of %q = %+v, want request %q index %d", encoded, got, requestID, idx)
			}
		}
	})

	t.Run("kinds", func(t *testing.T) {
		t.Parallel()
		for _, kind := range []string{kindFree, kindModal, kindText, kindSelect} {
			encoded := encodeKindID(requestID, kind)
			if err := checkCustomID(encoded); err != nil {
				t.Fatalf("checkCustomID(%q): %v", encoded, err)
			}
			got, ok := parseCustomID(encoded)
			if !ok {
				t.Fatalf("parseCustomID(%q) failed", encoded)
			}
			if got.RequestID != requestID || got.Kind != kind || got.Index != -1 {
				t.Fatalf("round trip of %q = %+v, want request %q kind %q", encoded, got, requestID, kind)
			}
		}
	})
}

func TestCustomIDBoundAtMaxIndex(t *testing.T) {
	t.Parallel()

	// A 12 hex character request id is the real shape; the bound must hold at
	// the highest index the domain allows.
	encoded := encodeChoiceID("abcdef012345", hitl.MaxChoices-1)
	if n := utf8.RuneCountInString(encoded); n > maxCustomIDLen {
		t.Fatalf("custom_id %q is %d characters, above the %d limit", encoded, n, maxCustomIDLen)
	}

	// A pathological request id must be rejected rather than sent as a 400.
	long := encodeChoiceID(strings.Repeat("f", maxCustomIDLen), hitl.MaxChoices-1)
	if err := checkCustomID(long); err == nil {
		t.Fatalf("checkCustomID(%d characters) = nil, want error", utf8.RuneCountInString(long))
	}
}

func TestParseCustomIDRejects(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"",
		"hitl",
		"hitl:abc",
		"hitl::free",
		"other:abc:free",
		"hitl:abc:bogus",
		"hitl:abc:c",
		"hitl:abc:c:x",
		"hitl:abc:c:-1",
		"hitl:abc:c:1:2",
		"hitl:abc:free:1",
	} {
		if got, ok := parseCustomID(raw); ok {
			t.Errorf("parseCustomID(%q) = %+v, true; want false", raw, got)
		}
	}
}

func TestBuildComponentsLayout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		choices  int
		freeText bool
		wantRows []int // component count per action row
		wantMenu bool
	}{
		{name: "single button", choices: 1, wantRows: []int{1}},
		{name: "full row", choices: 5, wantRows: []int{5}},
		{name: "spills to second row", choices: 6, wantRows: []int{5, 1}},
		{name: "free text joins a partial row", choices: 4, freeText: true, wantRows: []int{5}},
		{name: "free text opens a row when the last is full", choices: 5, freeText: true, wantRows: []int{5, 1}},
		{name: "free text only", choices: 0, freeText: true, wantRows: []int{1}},
		{name: "buttons fill four rows", choices: 20, wantRows: []int{5, 5, 5, 5}},
		{name: "buttons plus free text fill five rows", choices: 20, freeText: true, wantRows: []int{5, 5, 5, 5, 1}},
		{name: "past the threshold a menu takes over", choices: 21, freeText: true, wantRows: []int{1, 1}, wantMenu: true},
		{name: "menu without free text", choices: 21, wantRows: []int{1}, wantMenu: true},
		{name: "menu at the domain maximum", choices: hitl.MaxChoices, freeText: true, wantRows: []int{1, 1}, wantMenu: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := &hitl.Request{ID: "abcdef012345", Choices: choices(tt.choices), AllowFreeText: tt.freeText}
			rows, err := buildComponents(req)
			if err != nil {
				t.Fatalf("buildComponents: %v", err)
			}
			if len(rows) > maxActionRows {
				t.Fatalf("got %d action rows, above the %d limit", len(rows), maxActionRows)
			}
			if len(rows) != len(tt.wantRows) {
				t.Fatalf("got %d action rows, want %d", len(rows), len(tt.wantRows))
			}
			for i, row := range rows {
				ar, ok := row.(discordgo.ActionsRow)
				if !ok {
					t.Fatalf("row %d is %T, want discordgo.ActionsRow", i, row)
				}
				if len(ar.Components) != tt.wantRows[i] {
					t.Fatalf("row %d holds %d components, want %d", i, len(ar.Components), tt.wantRows[i])
				}
				if len(ar.Components) > maxButtonsPerRow {
					t.Fatalf("row %d holds %d components, above the %d limit", i, len(ar.Components), maxButtonsPerRow)
				}
			}

			first, ok := rows[0].(discordgo.ActionsRow)
			if !ok {
				t.Fatalf("row 0 is %T, want discordgo.ActionsRow", rows[0])
			}
			menu, isMenu := first.Components[0].(discordgo.SelectMenu)
			if isMenu != tt.wantMenu {
				t.Fatalf("row 0 select menu = %v, want %v", isMenu, tt.wantMenu)
			}
			if tt.wantMenu {
				if menu.MenuType != discordgo.StringSelectMenu {
					t.Errorf("menu type = %d, want %d", menu.MenuType, discordgo.StringSelectMenu)
				}
				if len(menu.Options) != tt.choices {
					t.Errorf("menu holds %d options, want %d", len(menu.Options), tt.choices)
				}
				if len(menu.Options) > maxSelectOptions {
					t.Errorf("menu holds %d options, above the %d limit", len(menu.Options), maxSelectOptions)
				}
				// Option values reuse the button encoding so one decoder covers
				// both layouts.
				for i, opt := range menu.Options {
					id, ok := parseCustomID(opt.Value)
					if !ok || id.Kind != kindChoice || id.Index != i {
						t.Fatalf("option %d value %q decodes to %+v (ok=%v)", i, opt.Value, id, ok)
					}
				}
			}

			if tt.freeText {
				last, ok := rows[len(rows)-1].(discordgo.ActionsRow)
				if !ok {
					t.Fatalf("last row is %T, want discordgo.ActionsRow", rows[len(rows)-1])
				}
				btn, ok := last.Components[len(last.Components)-1].(discordgo.Button)
				if !ok {
					t.Fatalf("last component is %T, want discordgo.Button", last.Components[len(last.Components)-1])
				}
				if btn.CustomID != encodeKindID(req.ID, kindFree) {
					t.Errorf("free text button custom_id = %q, want %q", btn.CustomID, encodeKindID(req.ID, kindFree))
				}
			}
		})
	}
}

func TestButtonStyles(t *testing.T) {
	t.Parallel()

	req := &hitl.Request{
		ID: "abcdef012345",
		Choices: []hitl.Choice{
			{ID: "yes", Label: "Yes", Style: hitl.StylePrimary},
			{ID: "no", Label: "No", Style: hitl.StyleDanger},
			{ID: "later", Label: "Later"},
		},
	}
	rows, err := buildComponents(req)
	if err != nil {
		t.Fatalf("buildComponents: %v", err)
	}
	row, ok := rows[0].(discordgo.ActionsRow)
	if !ok {
		t.Fatalf("row 0 is %T, want discordgo.ActionsRow", rows[0])
	}
	want := []discordgo.ButtonStyle{discordgo.SuccessButton, discordgo.DangerButton, discordgo.SecondaryButton}
	for i, comp := range row.Components {
		btn, ok := comp.(discordgo.Button)
		if !ok {
			t.Fatalf("component %d is %T, want discordgo.Button", i, comp)
		}
		if btn.Style != want[i] {
			t.Errorf("button %d style = %d, want %d", i, btn.Style, want[i])
		}
		// Link and premium buttons fire no interaction, so they must never
		// carry an answer.
		if btn.Style == discordgo.LinkButton || btn.Style == discordgo.PremiumButton {
			t.Errorf("button %d uses unreachable style %d", i, btn.Style)
		}
		if btn.CustomID == "" {
			t.Errorf("button %d has no custom_id", i)
		}
	}
}

func TestRenderContent(t *testing.T) {
	t.Parallel()

	req := &hitl.Request{
		ID:            "abcdef012345",
		Title:         "Deploy to production?",
		Body:          "The migration is irreversible.",
		AllowFreeText: true,
		Origin:        hitl.Origin{Agent: "claude", Cwd: "/home/huke/repo", Host: "wsl"},
	}

	content, truncated := renderContent(req, true)
	if truncated {
		t.Fatalf("short body reported as truncated")
	}
	for _, want := range []string{"Deploy to production?", "abcdef", "claude", "repo", "wsl", req.Body} {
		if !strings.Contains(content, want) {
			t.Errorf("content is missing %q:\n%s", want, content)
		}
	}
	if !strings.Contains(content, "Reply to this message") {
		t.Errorf("free-text question is missing the reply hint:\n%s", content)
	}

	// A notification carries no controls, so the reply hint is a lie.
	notice, _ := renderContent(req, false)
	if strings.Contains(notice, "Reply to this message") {
		t.Errorf("notification carries the reply hint:\n%s", notice)
	}
}

func TestRenderContentTruncates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "ascii", body: strings.Repeat("a", 5000)},
		{name: "multibyte", body: strings.Repeat("한", 3000)},
		{name: "just over the limit", body: strings.Repeat("b", maxContentRunes)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := &hitl.Request{ID: "abcdef012345", Title: "Long", Body: tt.body, AllowFreeText: true}
			content, truncated := renderContent(req, true)
			if !truncated {
				t.Fatalf("body of %d runes was not truncated", utf8.RuneCountInString(tt.body))
			}
			if n := utf8.RuneCountInString(content); n > maxContentRunes {
				t.Fatalf("content is %d runes, above the %d limit", n, maxContentRunes)
			}
			if !utf8.ValidString(content) {
				t.Fatalf("truncation split a rune")
			}
			if !strings.Contains(content, overflowName(req)) {
				t.Errorf("truncated content does not name the attached body:\n%s", content)
			}
		})
	}
}

func TestSettledContentKeepsTheOutcome(t *testing.T) {
	t.Parallel()

	req := &hitl.Request{ID: "abcdef012345", Title: "Long", Body: strings.Repeat("a", 5000), AllowFreeText: true}
	ans := &hitl.Answer{
		RequestID:   req.ID,
		Status:      hitl.StatusAnswered,
		ChoiceID:    "yes",
		ChoiceLabel: "Ship it",
		Text:        "Ship it",
		Responder:   hitl.Responder{Username: "huke"},
	}

	content := settledContent(req, ans)
	if n := utf8.RuneCountInString(content); n > maxContentRunes {
		t.Fatalf("settled content is %d runes, above the %d limit", n, maxContentRunes)
	}
	for _, want := range []string{"Answered:", "Ship it", "huke"} {
		if !strings.Contains(content, want) {
			t.Errorf("settled content is missing %q", want)
		}
	}

	timeout := settledContent(req, &hitl.Answer{RequestID: req.ID, Status: hitl.StatusTimeout})
	if !strings.Contains(timeout, "Timed out") {
		t.Errorf("timeout outcome missing:\n%s", timeout)
	}
	canceled := settledContent(req, &hitl.Answer{RequestID: req.ID, Status: hitl.StatusCanceled, Reason: "agent exited"})
	if !strings.Contains(canceled, "Canceled") || !strings.Contains(canceled, "agent exited") {
		t.Errorf("cancel outcome missing:\n%s", canceled)
	}
}

func TestFindTextInputWalksNestedComponents(t *testing.T) {
	t.Parallel()

	const target = "hitl:abcdef012345:text"

	// Discord decides the modal submission shape, not us: v0.29.0 unmarshals
	// components into pointers and may nest them inside containers.
	nested := []discordgo.MessageComponent{
		&discordgo.Container{Components: []discordgo.MessageComponent{
			&discordgo.Section{Components: []discordgo.MessageComponent{
				&discordgo.TextDisplay{Content: "not an answer"},
			}},
			&discordgo.ActionsRow{Components: []discordgo.MessageComponent{
				&discordgo.TextInput{CustomID: "hitl:abcdef012345:other", Value: "wrong"},
				&discordgo.TextInput{CustomID: target, Value: "ship it"},
			}},
		}},
	}

	got, ok := findTextInput(nested, target)
	if !ok || got != "ship it" {
		t.Fatalf("findTextInput(nested, %q) = %q, %v; want \"ship it\", true", target, got, ok)
	}

	// Value receivers must work too: they are what this package builds.
	flat := []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.TextInput{CustomID: target, Value: "flat answer"},
		}},
	}
	if got, ok := findTextInput(flat, target); !ok || got != "flat answer" {
		t.Fatalf("findTextInput(flat, %q) = %q, %v; want \"flat answer\", true", target, got, ok)
	}

	if got, ok := findTextInput(nested, "hitl:zzz:text"); ok {
		t.Fatalf("findTextInput with an unknown id = %q, true; want false", got)
	}

	// The empty id is the fallback: take the first input in tree order.
	if got, ok := findTextInput(nested, ""); !ok || got != "wrong" {
		t.Fatalf("findTextInput(nested, \"\") = %q, %v; want \"wrong\", true", got, ok)
	}

	if got, ok := findTextInput(nil, target); ok {
		t.Fatalf("findTextInput(nil) = %q, true; want false", got)
	}
}

func TestCorrelate(t *testing.T) {
	t.Parallel()

	const channel = "chan-1"
	one := []postedMessage{{RequestID: "req-1", ChannelID: channel, MessageID: "msg-1"}}
	two := []postedMessage{
		{RequestID: "req-1", ChannelID: channel, MessageID: "msg-1"},
		{RequestID: "req-2", ChannelID: channel, MessageID: "msg-2"},
	}

	tests := []struct {
		name    string
		posted  []postedMessage
		channel string
		replyTo string
		wantID  string
		want    correlation
	}{
		{name: "reply picks its question", posted: two, channel: channel, replyTo: "msg-2", wantID: "req-2", want: corrMatched},
		{name: "reply to something else", posted: two, channel: channel, replyTo: "msg-9", want: corrNone},
		{name: "single outstanding question", posted: one, channel: channel, wantID: "req-1", want: corrMatched},
		{name: "several outstanding questions refuse to guess", posted: two, channel: channel, want: corrAmbiguous},
		{name: "no outstanding questions", posted: nil, channel: channel, want: corrNone},
		{name: "other channel", posted: one, channel: "chan-2", want: corrNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotID, got := correlate(tt.posted, tt.channel, tt.replyTo)
			if got != tt.want || gotID != tt.wantID {
				t.Fatalf("correlate(%q, %q) = %q, %v; want %q, %v",
					tt.channel, tt.replyTo, gotID, got, tt.wantID, tt.want)
			}
		})
	}
}

func TestCheckUploadSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		attachments []hitl.Attachment
		extra       int64
		wantErr     bool
	}{
		{name: "empty"},
		{name: "within limits", attachments: []hitl.Attachment{{Filename: "a.png", Size: 1 << 20}}, extra: 4096},
		{
			name:        "one oversized file",
			attachments: []hitl.Attachment{{Filename: "big.png", Size: maxFileBytes + 1}},
			wantErr:     true,
		},
		{
			name: "sum over the message limit",
			attachments: []hitl.Attachment{
				{Filename: "a", Size: maxFileBytes},
				{Filename: "b", Size: maxFileBytes},
				{Filename: "c", Size: maxFileBytes},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkUploadSize(tt.attachments, tt.extra)
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkUploadSize = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
