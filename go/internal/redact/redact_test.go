// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package redact

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func setEnabled(t *testing.T, v bool) {
	t.Helper()
	SetEnabled(v)
	t.Cleanup(func() { SetEnabled(true) })
}

func TestValue(t *testing.T) {
	tests := []struct{ name, key, in, want string }{
		{"issue johnd", "user", "johnd", "jo***"},
		{"issue name", "userName", "Jane Doe", "jan***"},
		{"issue email", "email", "70779906+john-doe-sonarsource@users.noreply.github.com", "***joh***@users.noreply.github.com"},
		{"snake case", "user_name", "Jane Doe", "jan***"},
		{"kebab upper", "USER-NAME", "Jane Doe", "jan***"},
		{"login", "login", "abcdef", "ab***"},
		{"assignee", "assignee", "abcdefghi", "abc***"},
		{"scmAuthor", "scmAuthor", "abcdefghijkl", "abc***"},
		{"compound", "authorLogin", "abcdefghijkl", "abc***"},
		{"one rune", "user", "a", "***"},
		{"two runes", "user", "ab", "***"},
		{"three runes", "user", "abc", "a***"},
		{"unicode", "user", "éàüöñß", "éà***"},
		{"empty", "user", "", ""},
		{"email under user", "user", "jo@x.org", "******@x.org"},
		{"plain email", "author", "bob@example.com", "***b***@example.com"},
		{"code", "code", "fmt.Println(1)", "<redacted code, 14 bytes>"},
		{"code empty", "code", "", ""},
		{"non-sensitive", "project", "johnd", "johnd"},
		{"non-sensitive email", "name", "a@b.com", "a@b.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Value(tc.key, tc.in); got != tc.want {
				t.Fatalf("Value(%q,%q) = %q, want %q", tc.key, tc.in, got, tc.want)
			}
		})
	}
}

func TestText(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"none", "hello world", "hello world"},
		{"at only", "a @ b", "a @ b"},
		{"single", "contact john.doe@example.com now", "contact ***joh***@example.com now"},
		{"multiple", "a@b.io, 123+carol@users.noreply.github.com", "******@b.io, ***ca***@users.noreply.github.com"},
		{"url", "https://x/api?author=bob@corp.com&p=1", "https://x/api?author=***b***@corp.com&p=1"},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Text(tc.in); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestJSON(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"flat", `{"user":"johnd","n":12345678901234567890,"ok":true,"x":null}`, `{"user":"jo***","n":12345678901234567890,"ok":true,"x":null}`},
		{"nested", `{"a":[{"email":"bob@x.com","login":"abcdef"}],"b":{"assignee":"Jane Doe"}}`, `{"a":[{"email":"***b***@x.com","login":"ab***"}],"b":{"assignee":"jan***"}}`},
		{"non-string sensitive", `{"user":5,"author":false,"code":{"author":"abc"}}`, `{"user":5,"author":false,"code":{"author":"a***"}}`},
		{"code string", `{"code":"x := 1"}`, `{"code":"<redacted code, 6 bytes>"}`},
		{"free text email", `{"msg":"mail bob@x.com <ok> & done","list":["a@b.io"]}`, `{"msg":"mail ***b***@x.com <ok> & done","list":["******@b.io"]}`},
		{"changelog", `{"changelog":[{"user":"jdoe1","userName":"Jane Doe","diffs":[{"key":"assignee","oldValue":"alice","newValue":"bobby"},{"key":"status","oldValue":"OPEN","newValue":"x@y.com"}]}]}`,
			`{"changelog":[{"user":"jd***","userName":"jan***","diffs":[{"key":"assignee","oldValue":"al***","newValue":"bo***"},{"key":"status","oldValue":"OPEN","newValue":"******@y.com"}]}]}`},
		{"diff key after values", `{"oldValue":"alice","key":"author"}`, `{"oldValue":"al***","key":"author"}`},
		{"scm ignored in JSON", `{"scm":[[1,"alice","d","r"]]}`, `{"scm":[[1,"alice","d","r"]]}`},
		{"user display name", `{"login":"jdoe1","name":"Jane Doe","externalIdentity":"jdoe-ext","managed":false}`,
			`{"login":"jd***","name":"jan***","externalIdentity":"jdo***","managed":false}`},
		{"name without login untouched", `{"key":"grp","name":"Anyone"}`, `{"key":"grp","name":"Anyone"}`},
		{"submitter and note login", `{"submitterLogin":"admin","params":[{"noteLogin":"admin"}]}`, `{"submitterLogin":"ad***","params":[{"noteLogin":"ad***"}]}`},
		{"comments", `{"comments":[{"markdown":"secret *text*","htmlText":"secret <em>text</em>","login":"bob"}],"comment":"hi","comment2":[]}`,
			`{"comments":[{"markdown":"<redacted comment, 13 bytes>","htmlText":"<redacted comment, 20 bytes>","login":"b***"}],"comment":"<redacted comment, 2 bytes>","comment2":[]}`},
		{"home path and alm", `{"p":"/Users/jdoe/code","a":"Alm:github, App Id:946159, Client Id:Iv23ligl0iLhGRRvwFGO","z":"Url:https://dev.azure.com/acme-org"}`,
			`{"p":"/Users/***/code","a":"Alm:github, App Id:94***, Client Id:iv2***","z":"Url:https://dev.azure.com/***"}`},
		{"empty array", `[]`, `[]`},
		{"escapes", "{\"s\":\"a\\n\\\"q\\\"\"}", "{\"s\":\"a\\n\\\"q\\\"\"}"},
		{"invalid", `{"user":`, `{"user":`},
		{"trailing", `{"user":"abc"} x`, `{"user":"abc"} x`},
		{"not json", `plain`, `plain`},
		{"empty", ``, ``},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(JSON([]byte(tc.in))); got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestSCMJSON(t *testing.T) {
	in := `{"scm":[[1,"alice@x.com","2020","abc"],[2,"Bob Smith","2020","def"],[3],[4,7],"x"],"user":"jdoe1"}`
	want := `{"scm":[[1,"***al***@x.com","2020","abc"],[2,"bob***","2020","def"],[3],[4,7],"x"],"user":"jd***"}`
	if got := string(SCMJSON([]byte(in))); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got := string(SCMJSON([]byte(`{"scm":"nope"}`))); got != `{"scm":"nope"}` {
		t.Fatalf("unexpected %s", got)
	}
}

func TestJSONLarge(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < 50000; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"author":"someone","n":1}`)
	}
	sb.WriteString("]")
	if got := JSON([]byte(sb.String())); bytes.Contains(got, []byte("someone")) {
		t.Fatal("author not redacted")
	}
}

func TestSourceCode(t *testing.T) {
	if got := string(SourceCode([]byte("package main"))); got != "<redacted source code, 12 bytes>" {
		t.Fatalf("got %q", got)
	}
}

func TestDisabledNoOps(t *testing.T) {
	setEnabled(t, false)
	if Enabled() {
		t.Fatal("should be disabled")
	}
	if Value("user", "johnd") != "johnd" || Text("a@b.com") != "a@b.com" {
		t.Fatal("string funcs must be no-ops")
	}
	body := []byte(`{"user":"johnd"}`)
	if string(JSON(body)) != string(body) || string(SCMJSON(body)) != string(body) || string(SourceCode(body)) != string(body) {
		t.Fatal("byte funcs must be no-ops")
	}
}

func newBufLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(NewHandler(slog.NewTextHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(g []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey && len(g) == 0 {
				return slog.Attr{}
			}
			return a
		},
	})))
}

func TestHandler(t *testing.T) {
	var buf bytes.Buffer
	l := newBufLogger(&buf)
	l.Info("sent to bob@x.com", "user", "johnd", "project", "p1", "err", errors.New("fail for amy@y.org"),
		"url", "http://h/?e=zed@z.io", "n", 3, slog.Group("g", "email", "carol@c.com", "login", "abcdef"))
	l.With("author", "Jane Doe").WithGroup("grp").Warn("w", "assignee", "abcdefghi", "code", "x := 1")
	out := buf.String()
	for _, leak := range []string{"bob@x.com", "johnd", "amy@y.org", "zed@z.io", "carol@c.com", "g.login=abcdef", "Jane Doe", "abcdefghi", "x := 1"} {
		if strings.Contains(out, leak) {
			t.Errorf("leaked %q in %s", leak, out)
		}
	}
	for _, want := range []string{"level=INFO", "level=WARN", "project=p1", "n=3", "user=jo***", "***b***@x.com", "g.email=***ca***@c.com", "grp.assignee=abc***", "author=jan***", "grp.code=\"<redacted code, 6 bytes>\""} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
}

func TestHandlerDisabledAndLateFlip(t *testing.T) {
	var buf bytes.Buffer
	l := newBufLogger(&buf).With("user", "johnd")
	l.Info("m a@b.com")
	if strings.Contains(buf.String(), "johnd") {
		t.Fatalf("should be redacted: %s", buf.String())
	}
	setEnabled(t, false)
	buf.Reset()
	l.Info("m a@b.com", "email", "x@y.com")
	out := buf.String()
	for _, want := range []string{"user=johnd", "m a@b.com", "email=x@y.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
}

func TestHandlerMisc(t *testing.T) {
	var buf bytes.Buffer
	h := NewHandler(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if h.Enabled(context.Background(), slog.LevelInfo) || !h.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("Enabled must delegate")
	}
	if h.WithAttrs(nil) != h || h.WithGroup("") != h {
		t.Fatal("empty WithAttrs/WithGroup must return same handler")
	}
	slog.New(h).Error("x", "v", slog.AnyValue(struct{ A int }{1}), "s", any("a@b.com"), "nilv", nil)
	if !strings.Contains(buf.String(), "******@b.com") {
		t.Fatalf("got %s", buf.String())
	}
}

type nilPanicErr struct{ msg string }

func (e *nilPanicErr) Error() string { return e.msg }

func TestHandlerTypedNilError(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(NewHandler(slog.NewTextHandler(&buf, nil)))
	var err *nilPanicErr
	l.Info("boom", "err", err)
	if !strings.Contains(buf.String(), "err=") {
		t.Fatalf("typed-nil error not logged: %q", buf.String())
	}
}

func TestForm(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"login", "login=okorach-sonar-kgZGw%40github&name=migration-viewers&organization=latest-gh", "login=oko%2A%2A%2A&name=migration-viewers&organization=latest-gh"},
		{"comment text", "issue=AaIT&text=%5BMigrated+from+admin%5D%0A%0ABecause+I+am+lazy", "issue=AaIT&text=%3Credacted+comment%2C+40+bytes%3E"},
		{"encoded email in url", "name=Home&url=olivier.korach%40sonarsource.com", "name=Home&url=%2A%2A%2Aoli%2A%2A%2A%40sonarsource.com"},
		{"untouched keeps encoding", "description=Test+permission+%28group%29&name=x", "description=Test+permission+%28group%29&name=x"},
		{"markdown_note", "markdown_note=secret", "markdown_note=%3Credacted+comment%2C+6+bytes%3E"},
		{"empty text", "text=&a=b", "text=&a=b"},
		{"not a form", "plain text", "plain text"},
		{"json not a form", `{"a":"b=c"}`, `{"a":"b=c"}`},
		{"multipart not a form", "--abc\r\nContent-Disposition: x=y", "--abc\r\nContent-Disposition: x=y"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(Form([]byte(tc.in))); got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
	setEnabled(t, false)
	if got := string(Form([]byte("login=bob"))); got != "login=bob" {
		t.Fatalf("disabled: got %s", got)
	}
}
