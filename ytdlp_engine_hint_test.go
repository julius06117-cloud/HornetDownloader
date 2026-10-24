package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestYTDLPTrimErrHintAndUTF8(t *testing.T) {
	got := trimErr("ERROR: [youtube] abc: Sign in to confirm you’re not a bot. Use --cookies-from-browser")
	if !strings.HasPrefix(got, "网站要求登录验证") || !strings.Contains(got, "you’re") {
		t.Fatalf("hint missing or text mangled: %q", got)
	}
	long := strings.Repeat("错", 300)
	if g := trimErr(long); !utf8.ValidString(g) || utf8.RuneCountInString(g) != 201 {
		t.Fatalf("bad truncation: %d runes valid=%v", utf8.RuneCountInString(g), utf8.ValidString(g))
	}
	if g := trimErr("plain error"); g != "plain error" {
		t.Fatalf("unexpected change: %q", g)
	}
}
