package telegram

import (
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/huketo/herdr-hitl/internal/hitl"
)

func TestTruncateUTF16(t *testing.T) {
	t.Parallel()

	// U+1F600 GRINNING FACE is outside the BMP: one rune, two UTF-16 units.
	const emoji = "\U0001F600"

	tests := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{name: "empty", in: "", limit: 10, want: ""},
		{name: "fits exactly", in: "abcde", limit: 5, want: "abcde"},
		{name: "one over", in: "abcde", limit: 4, want: "abcd"},
		{name: "zero limit", in: "abcde", limit: 0, want: ""},
		{name: "negative limit", in: "abcde", limit: -3, want: ""},
		{name: "bmp rune costs one", in: "héllo", limit: 2, want: "hé"},
		{name: "emoji costs two", in: emoji + emoji, limit: 4, want: emoji + emoji},
		{name: "emoji does not split", in: emoji + emoji, limit: 3, want: emoji},
		{name: "emoji alone over budget", in: emoji, limit: 1, want: ""},
		{name: "emoji after text", in: "ab" + emoji, limit: 3, want: "ab"},
		{name: "emoji after text fits", in: "ab" + emoji, limit: 4, want: "ab" + emoji},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := truncateUTF16(tc.in, tc.limit); got != tc.want {
				t.Fatalf("truncateUTF16(%q, %d) = %q, want %q", tc.in, tc.limit, got, tc.want)
			}
		})
	}
}

func TestUTF16Len(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want int
	}{
		{name: "ascii", in: "hello", want: 5},
		{name: "bmp", in: "안녕", want: 2},
		{name: "astral", in: "\U0001F600", want: 2},
		{name: "mixed", in: "a\U0001F600b", want: 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := utf16Len(tc.in); got != tc.want {
				t.Fatalf("utf16Len(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestEscapeHTML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "clean text is untouched", in: "deploy to prod?", want: "deploy to prod?"},
		{name: "script tag", in: "<script>alert(1)</script>", want: "&lt;script&gt;alert(1)&lt;/script&gt;"},
		{name: "ampersand", in: "a & b", want: "a &amp; b"},
		{name: "entity is escaped once", in: "&amp;", want: "&amp;amp;"},
		{name: "backticks survive", in: "`rm -rf /`", want: "`rm -rf /`"},
		// HTML parse mode is chosen precisely so these need no escaping.
		{name: "markdownv2 specials survive", in: "_*[]()~#+-=|{}.!", want: "_*[]()~#+-=|{}.!"},
		{name: "closing bold injection", in: "</b><b>", want: "&lt;/b&gt;&lt;b&gt;"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := escapeHTML(tc.in); got != tc.want {
				t.Fatalf("escapeHTML(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRenderBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain text is escaped",
			in:   "pick <one> & go",
			want: "pick &lt;one&gt; &amp; go",
		},
		{
			name: "fenced block becomes pre code",
			in:   "before\n```go\nif a < b {}\n```\nafter",
			want: "before\n<pre><code>if a &lt; b {}</code></pre>\nafter",
		},
		{
			name: "unterminated fence still closes",
			in:   "before\n```\nx < y",
			want: "before\n<pre><code>x &lt; y</code></pre>",
		},
		{
			name: "crlf is normalised",
			in:   "a\r\nb",
			want: "a\nb",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := renderBody(tc.in); got != tc.want {
				t.Fatalf("renderBody(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRenderBodyBalancesTags(t *testing.T) {
	t.Parallel()

	got := renderBody("a\n```\nb\n```\nc\n```\nd\n")
	if opens, closes := strings.Count(got, "<pre><code>"), strings.Count(got, "</code></pre>"); opens != closes {
		t.Fatalf("unbalanced tags in %q: %d opens, %d closes", got, opens, closes)
	}
}

func TestComposeQuestion(t *testing.T) {
	t.Parallel()

	t.Run("header carries id and origin", func(t *testing.T) {
		t.Parallel()
		req := &hitl.Request{
			ID:    "abc123def456",
			Title: "Deploy <prod> & pray",
			Body:  "ok?",
			Origin: hitl.Origin{
				Agent: "claude",
				Host:  "dev-box",
			},
			AllowFreeText: true,
		}
		q := composeQuestion(req, nil, true)
		if q.Overflow {
			t.Fatal("short question should not overflow")
		}
		for _, want := range []string{
			"<code>abc123def456</code>",
			"Deploy &lt;prod&gt; &amp; pray",
			"claude · dev-box",
			"Reply to this message",
		} {
			if !strings.Contains(q.Text, want) {
				t.Fatalf("composed text %q missing %q", q.Text, want)
			}
		}
	})

	t.Run("failed uploads are reported", func(t *testing.T) {
		t.Parallel()
		req := &hitl.Request{ID: "id1", Title: "t", Body: "b"}
		q := composeQuestion(req, []string{"shot<1>.png"}, true)
		if !strings.Contains(q.Text, "Could not upload: shot&lt;1&gt;.png") {
			t.Fatalf("missing upload failure note: %q", q.Text)
		}
	})

	t.Run("overlong body is truncated and flagged", func(t *testing.T) {
		t.Parallel()
		req := &hitl.Request{
			ID:    "id2",
			Title: "long",
			Body:  strings.Repeat("word <&> ", 4000),
		}
		q := composeQuestion(req, nil, true)
		if !q.Overflow {
			t.Fatal("oversized body should set Overflow")
		}
		if n := utf16Len(q.Text); n > maxQuestionUnits {
			t.Fatalf("composed text is %d units, over the %d budget", n, maxQuestionUnits)
		}
		if !strings.Contains(q.Text, "…") {
			t.Fatalf("truncated text should carry the ellipsis marker: %q", q.Text)
		}
		if !strings.Contains(q.Text, "attached as a document") {
			t.Fatalf("truncated text should point at the attachment: %q", q.Text)
		}
	})

	t.Run("settled question still fits a telegram message", func(t *testing.T) {
		t.Parallel()
		req := &hitl.Request{
			ID:    "id3",
			Title: strings.Repeat("t", hitl.MaxTitleLen),
			Body:  strings.Repeat("\U0001F600", 5000),
		}
		q := composeQuestion(req, nil, true)
		settled := q.Text + "\n\n" + outcomeLine(&hitl.Answer{
			Status:      hitl.StatusAnswered,
			ChoiceLabel: strings.Repeat("l", 200),
			Responder:   hitl.Responder{Username: strings.Repeat("u", 200)},
		})
		if n := utf16Len(settled); n > maxMessageUnits {
			t.Fatalf("settled text is %d units, over the %d cap", n, maxMessageUnits)
		}
	})
}

func TestCallbackData(t *testing.T) {
	t.Parallel()

	t.Run("stays inside the 64 byte cap", func(t *testing.T) {
		t.Parallel()
		// The widest realistic payload: a full-length id and the highest
		// index the keyboard can hold.
		id := hitl.NewID()
		for _, index := range []int{0, 9, hitl.MaxChoices - 1, maxButtons - 1} {
			data, ok := callbackData(id, index)
			if !ok {
				t.Fatalf("callbackData(%q, %d) rejected", id, index)
			}
			if len(data) > maxCallbackDataBytes {
				t.Fatalf("callbackData(%q, %d) = %q is %d bytes, over %d", id, index, data, len(data), maxCallbackDataBytes)
			}
		}
	})

	t.Run("rejects an oversized id", func(t *testing.T) {
		t.Parallel()
		if _, ok := callbackData(strings.Repeat("x", 64), 1); ok {
			t.Fatal("expected an oversized payload to be rejected")
		}
	})
}

func TestParseCallbackData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		in        string
		wantID    string
		wantIndex int
		wantOK    bool
	}{
		{name: "round trip", in: "abc123def456:7", wantID: "abc123def456", wantIndex: 7, wantOK: true},
		{name: "index zero", in: "abc:0", wantID: "abc", wantIndex: 0, wantOK: true},
		{name: "splits on the last colon", in: "a:b:3", wantID: "a:b", wantIndex: 3, wantOK: true},
		{name: "no colon", in: "abc", wantOK: false},
		{name: "empty index", in: "abc:", wantOK: false},
		{name: "empty id", in: ":3", wantOK: false},
		{name: "non numeric index", in: "abc:x", wantOK: false},
		{name: "negative index", in: "abc:-1", wantOK: false},
		{name: "empty", in: "", wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id, index, ok := parseCallbackData(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("parseCallbackData(%q) ok = %v, want %v", tc.in, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if id != tc.wantID || index != tc.wantIndex {
				t.Fatalf("parseCallbackData(%q) = (%q, %d), want (%q, %d)", tc.in, id, index, tc.wantID, tc.wantIndex)
			}
		})
	}
}

func TestCallbackDataRoundTripsEveryChoice(t *testing.T) {
	t.Parallel()

	id := hitl.NewID()
	for i := range hitl.MaxChoices {
		data, ok := callbackData(id, i)
		if !ok {
			t.Fatalf("callbackData rejected index %d", i)
		}
		gotID, gotIndex, ok := parseCallbackData(data)
		if !ok || gotID != id || gotIndex != i {
			t.Fatalf("round trip of %q gave (%q, %d, %v)", data, gotID, gotIndex, ok)
		}
	}
}

func TestChoiceRows(t *testing.T) {
	t.Parallel()

	choices := func(n int) []hitl.Choice {
		out := make([]hitl.Choice, n)
		for i := range n {
			out[i] = hitl.Choice{ID: "c" + string(rune('a'+i%26)), Label: "Choice"}
		}
		return out
	}

	tests := []struct {
		name     string
		count    int
		perRow   int
		wantRows []int // button count per row
	}{
		{name: "none", count: 0, perRow: 3},
		{name: "one", count: 1, perRow: 3, wantRows: []int{1}},
		{name: "exact row", count: 3, perRow: 3, wantRows: []int{3}},
		{name: "remainder row", count: 4, perRow: 3, wantRows: []int{3, 1}},
		{name: "two full rows", count: 6, perRow: 3, wantRows: []int{3, 3}},
		{name: "per row clamped up from zero", count: 2, perRow: 0, wantRows: []int{1, 1}},
		{name: "per row clamped to eight", count: 9, perRow: 20, wantRows: []int{8, 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rows := choiceRows("req1", choices(tc.count), tc.perRow)
			if len(rows) != len(tc.wantRows) {
				t.Fatalf("got %d rows, want %d", len(rows), len(tc.wantRows))
			}
			for i, want := range tc.wantRows {
				if len(rows[i]) != want {
					t.Fatalf("row %d has %d buttons, want %d", i, len(rows[i]), want)
				}
			}
		})
	}
}

func TestChoiceRowsCapsTotalButtons(t *testing.T) {
	t.Parallel()

	many := make([]hitl.Choice, 150)
	for i := range many {
		many[i] = hitl.Choice{ID: "c", Label: "l"}
	}
	total := 0
	for _, row := range choiceRows("req1", many, buttonsPerRow) {
		total += len(row)
	}
	if total != maxButtons {
		t.Fatalf("kept %d buttons, want the %d cap", total, maxButtons)
	}
}

func TestKeyboardStylesAndPayloads(t *testing.T) {
	t.Parallel()

	req := &hitl.Request{
		ID: "req1",
		Choices: []hitl.Choice{
			{ID: "ship", Label: "Ship it", Style: hitl.StylePrimary},
			{ID: "wait", Label: "Wait"},
			{ID: "drop", Label: "Drop the table", Style: hitl.StyleDanger},
		},
	}
	markup := keyboard(req, true)
	kb, ok := markup.(models.InlineKeyboardMarkup)
	if !ok {
		t.Fatalf("keyboard() = %T, want models.InlineKeyboardMarkup", markup)
	}
	got := kb.InlineKeyboard[0]
	wantStyles := []string{"success", "", "danger"}
	for i, b := range got {
		if b.Style != wantStyles[i] {
			t.Fatalf("button %d style = %q, want %q", i, b.Style, wantStyles[i])
		}
		if b.CallbackData != "req1:"+string(rune('0'+i)) {
			t.Fatalf("button %d callback data = %q", i, b.CallbackData)
		}
		if strings.Contains(b.CallbackData, b.Text) {
			t.Fatalf("button %d leaks its label into callback_data: %q", i, b.CallbackData)
		}
	}
	if kb.ForceReply {
		t.Error("a buttons-only question must not force a reply")
	}

	if keyboard(&hitl.Request{ID: "req2"}, true) != nil {
		t.Fatal("a request with neither choices nor free text must not get markup")
	}
}

func TestKeyboardOffersATextBoxWhenFreeTextIsAllowed(t *testing.T) {
	t.Parallel()

	// force_reply is what makes the client pre-fill the reply target. Without
	// it an incoming message has no reply_to_message, and two concurrent
	// questions in one chat both become unanswerable by text.
	freeOnly := keyboard(&hitl.Request{ID: "req3", AllowFreeText: true}, true)
	force, ok := freeOnly.(models.ForceReply)
	if !ok {
		t.Fatalf("free-text-only markup = %T, want models.ForceReply", freeOnly)
	}
	if !force.ForceReply || !force.Selective || force.InputFieldPlaceholder == "" {
		t.Fatalf("force reply = %+v, want it enabled, selective, and carries a placeholder", force)
	}

	both := keyboard(&hitl.Request{
		ID:            "req4",
		AllowFreeText: true,
		Choices:       []hitl.Choice{{ID: "yes", Label: "Yes"}},
	}, true)
	kb, ok := both.(models.InlineKeyboardMarkup)
	if !ok {
		t.Fatalf("mixed markup = %T, want models.InlineKeyboardMarkup", both)
	}
	if !kb.ForceReply {
		t.Error("a question offering both buttons and free text must force a reply")
	}
	if len(kb.InlineKeyboard) == 0 {
		t.Error("the buttons went missing")
	}
}

func TestKeyboardOmitsForceReplyInAChannel(t *testing.T) {
	t.Parallel()

	// Telegram answers any non-inline reply markup sent to a channel with
	// "400 Bad Request: inline keyboard expected", which fails the whole
	// question rather than degrading it. A channel gets buttons or nothing.
	if got := keyboard(&hitl.Request{ID: "req5", AllowFreeText: true}, false); got != nil {
		t.Fatalf("free-text-only markup in a channel = %T, want nil", got)
	}

	mixed := keyboard(&hitl.Request{
		ID:            "req6",
		AllowFreeText: true,
		Choices:       []hitl.Choice{{ID: "yes", Label: "Yes"}},
	}, false)
	kb, ok := mixed.(models.InlineKeyboardMarkup)
	if !ok {
		t.Fatalf("mixed markup in a channel = %T, want models.InlineKeyboardMarkup", mixed)
	}
	if kb.ForceReply {
		t.Error("force_reply must not be set on a channel message")
	}
	if len(kb.InlineKeyboard) == 0 {
		t.Error("the buttons went missing")
	}
}

func TestQuestionFooterPromisesNoReplyBoxInAChannel(t *testing.T) {
	t.Parallel()

	req := &hitl.Request{ID: "req7", Title: "Ship?", Body: "yes or no", AllowFreeText: true}
	if got := questionFooter(req, nil, false); strings.Contains(got, "reply") {
		t.Errorf("channel footer = %q, want no promise of a reply box", got)
	}
	if got := questionFooter(req, nil, true); !strings.Contains(got, "Reply to this message") {
		t.Errorf("private-chat footer = %q, want the reply hint", got)
	}
}

func TestOutcomeLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ans  *hitl.Answer
		want string
	}{
		{
			name: "answered names the responder and the choice",
			ans: &hitl.Answer{
				Status:      hitl.StatusAnswered,
				ChoiceLabel: "Ship <it>",
				Responder:   hitl.Responder{Username: "@huke"},
			},
			want: "<b>Answered</b> by @huke: Ship &lt;it&gt;",
		},
		{
			name: "timeout carries the reason",
			ans:  &hitl.Answer{Status: hitl.StatusTimeout, Reason: "no answer within 30m"},
			want: "<b>Timed out</b> — no answer within 30m",
		},
		{
			name: "cancel falls back to a default reason",
			ans:  &hitl.Answer{Status: hitl.StatusCanceled},
			want: "<b>Canceled</b> — withdrawn",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := outcomeLine(tc.ans); got != tc.want {
				t.Fatalf("outcomeLine = %q, want %q", got, tc.want)
			}
		})
	}
}
