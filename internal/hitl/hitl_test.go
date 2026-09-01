package hitl_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huketo/herdr-hitl/internal/hitl"
)

func TestRequestValidate(t *testing.T) {
	t.Parallel()

	valid := func(mutate func(*hitl.Request)) *hitl.Request {
		req := &hitl.Request{
			Title:   "Deploy?",
			Body:    "Ship main to production?",
			Choices: []hitl.Choice{{ID: "yes", Label: "Yes"}},
		}
		if mutate != nil {
			mutate(req)
		}
		return req
	}

	tests := []struct {
		name    string
		req     *hitl.Request
		wantErr string
	}{
		{
			name: "choices only",
			req:  valid(nil),
		},
		{
			name: "free text only",
			req: valid(func(r *hitl.Request) {
				r.Choices = nil
				r.AllowFreeText = true
			}),
		},
		{
			name:    "no title and no body",
			req:     valid(func(r *hitl.Request) { r.Title, r.Body = "", "  " }),
			wantErr: "needs a title or a body",
		},
		{
			// A question with neither buttons nor a text box can never be
			// answered; failing here beats hanging until the deadline.
			name:    "unanswerable",
			req:     valid(func(r *hitl.Request) { r.Choices = nil }),
			wantErr: "offers no choices and forbids free text",
		},
		{
			name: "duplicate choice id",
			req: valid(func(r *hitl.Request) {
				r.Choices = append(r.Choices, hitl.Choice{ID: "yes", Label: "Yes again"})
			}),
			wantErr: `duplicate choice id "yes"`,
		},
		{
			name:    "empty choice label",
			req:     valid(func(r *hitl.Request) { r.Choices[0].Label = " " }),
			wantErr: "empty label",
		},
		{
			name:    "empty choice id",
			req:     valid(func(r *hitl.Request) { r.Choices[0].ID = "" }),
			wantErr: "empty id",
		},
		{
			name:    "unknown style",
			req:     valid(func(r *hitl.Request) { r.Choices[0].Style = "rainbow" }),
			wantErr: "unknown style",
		},
		{
			name:    "choice label too long for a Discord button",
			req:     valid(func(r *hitl.Request) { r.Choices[0].Label = strings.Repeat("x", hitl.MaxChoiceLabelLen+1) }),
			wantErr: "exceeds 80 characters",
		},
		{
			name: "too many choices",
			req: valid(func(r *hitl.Request) {
				r.Choices = make([]hitl.Choice, 0, hitl.MaxChoices+1)
				for i := range hitl.MaxChoices + 1 {
					r.Choices = append(r.Choices, hitl.Choice{ID: string(rune('a' + i)), Label: "x"})
				}
			}),
			wantErr: "exceeds the limit of 25",
		},
		{
			name:    "negative timeout",
			req:     valid(func(r *hitl.Request) { r.Timeout = -1 }),
			wantErr: "must not be negative",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.req.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("Validate() = nil, want error containing %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestNewAttachmentClassifies(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write := func(name string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	tests := []struct {
		file      string
		wantKind  hitl.AttachmentKind
		wantMedia string
	}{
		{"shot.PNG", hitl.KindImage, "image/png"},
		{"photo.jpeg", hitl.KindImage, "image/jpeg"},
		{"plan.md", hitl.KindDocument, "text/markdown"},
		{"report.html", hitl.KindDocument, "text/html"},
		{"change.patch", hitl.KindDocument, "text/x-diff"},
		{"blob.unknownext", hitl.KindDocument, "application/octet-stream"},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()
			att, err := hitl.NewAttachment(write(tc.file))
			if err != nil {
				t.Fatalf("NewAttachment: %v", err)
			}
			if att.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", att.Kind, tc.wantKind)
			}
			if att.MediaType != tc.wantMedia {
				t.Errorf("MediaType = %q, want %q", att.MediaType, tc.wantMedia)
			}
			if att.Filename != tc.file {
				t.Errorf("Filename = %q, want %q", att.Filename, tc.file)
			}
			if !filepath.IsAbs(att.Path) {
				t.Errorf("Path = %q, want absolute", att.Path)
			}
			if att.Size != int64(len("payload")) {
				t.Errorf("Size = %d, want %d", att.Size, len("payload"))
			}
		})
	}
}

func TestNewAttachmentRejectsBadInput(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	oversized := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(oversized, make([]byte, hitl.MaxAttachmentBytes+1), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{"missing", filepath.Join(dir, "nope.md"), "no such file"},
		{"directory", dir, "is a directory"},
		{"empty", empty, "file is empty"},
		{"oversized", oversized, "exceeds the"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := hitl.NewAttachment(tc.path)
			if err == nil {
				t.Fatalf("NewAttachment(%q) = nil, want error", tc.path)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestOriginLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		origin hitl.Origin
		want   string
	}{
		{
			name:   "agent and workspace beat the cwd",
			origin: hitl.Origin{Agent: "claude", WorkspaceLabel: "api", Cwd: "/srv/other", Host: "workbox"},
			want:   "claude · api · workbox",
		},
		{
			name:   "cwd basename stands in for a missing workspace label",
			origin: hitl.Origin{Agent: "codex", Cwd: "/home/huke/projects/herdr-hitl"},
			want:   "codex · herdr-hitl",
		},
		{
			name:   "empty origin renders empty",
			origin: hitl.Origin{},
			want:   "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.origin.Label(); got != tc.want {
				t.Errorf("Label() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAnswerErrMapsStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status  hitl.Status
		wantErr error
	}{
		{hitl.StatusAnswered, nil},
		{hitl.StatusTimeout, hitl.ErrTimeout},
		{hitl.StatusCanceled, hitl.ErrCanceled},
	}
	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			t.Parallel()
			ans := &hitl.Answer{Status: tc.status}
			if got := ans.Err(); got != tc.wantErr { //nolint:errorlint // sentinel identity is the point
				t.Errorf("Err() = %v, want %v", got, tc.wantErr)
			}
		})
	}
	var nilAnswer *hitl.Answer
	if got := nilAnswer.Err(); got != hitl.ErrUnknownRequest { //nolint:errorlint // sentinel identity is the point
		t.Errorf("nil answer Err() = %v, want ErrUnknownRequest", got)
	}
}

func TestResponderDisplayPrefersTheMostSpecificName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		responder hitl.Responder
		want      string
	}{
		{hitl.Responder{Transport: "telegram", UserID: "111", Username: "huke"}, "huke"},
		{hitl.Responder{Transport: "telegram", UserID: "111"}, "111"},
		{hitl.Responder{Transport: "telegram"}, "telegram"},
		{hitl.Responder{}, "unknown"},
	}
	for _, tc := range tests {
		if got := tc.responder.Display(); got != tc.want {
			t.Errorf("Display(%+v) = %q, want %q", tc.responder, got, tc.want)
		}
	}
}
