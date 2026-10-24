package main

import "testing"

func TestRecommendEngine(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"magnet:?xt=urn:btih:abc", EngineBT},
		{"https://example.com/a.torrent", EngineBT},
		{"https://mirrors.tuna.tsinghua.edu.cn/ubuntu/ubuntu-24.04.iso", EngineAria2},
		{"https://www.bilibili.com/video/BV1xx", EngineYTDLP},
		{"https://youtu.be/abcdef", EngineYTDLP},
		{"https://example.com/file.zip", EngineAria2},
	}
	for _, c := range cases {
		got, _ := RecommendEngine(c.in)
		if got != c.want {
			t.Fatalf("%q: got %s want %s", c.in, got, c.want)
		}
	}
}

func TestResolveEngine(t *testing.T) {
	mag := "magnet:?xt=urn:btih:7ACF8FB590B2060DD9C3146EF770169D593433B0"
	cases := []struct {
		name, req, uri, pref, want string
		wantErr                    bool
	}{
		{"auto magnet -> bt", "auto", mag, "auto", EngineBT, false},
		// 回归：旧版本存的默认 aria2 不能覆盖显式「自动」
		{"auto ignores saved default aria2", "auto", mag, "aria2", EngineBT, false},
		{"auto video -> ytdlp even if default aria2", "auto", "https://www.bilibili.com/video/BV1x", "aria2", EngineYTDLP, false},
		{"auto http -> aria2 even if default bt", "auto", "https://mirrors.tuna.tsinghua.edu.cn/a.iso", "bt", EngineAria2, false},
		{"auto torrent upload -> bt", "auto", "debian.torrent", "aria2", EngineBT, false},
		{"manual bt honored", "bt", mag, "aria2", EngineBT, false},
		{"manual aria2 honored for magnet", "aria2", mag, "auto", EngineAria2, false},
		{"manual ytdlp honored", "ytdlp", "https://example.com/page", "auto", EngineYTDLP, false},
		{"manual bt on http iso -> error", "bt", "https://a.com/x.iso", "auto", "", true},
		{"manual ytdlp on magnet -> error", "ytdlp", mag, "auto", "", true},
		{"manual bt for http .torrent ok", "bt", "https://a.com/x.torrent", "auto", EngineBT, false},
		{"label alias BitTorrent", "BitTorrent", mag, "auto", EngineBT, false},
		{"default uses settings pref", "", mag, "aria2", EngineAria2, false},
		{"default pref unsupported -> recommendation", "", "https://a.com/x.iso", "bt", EngineAria2, false},
		{"default pref auto -> recommendation", "default", mag, "auto", EngineBT, false},
		{"unknown engine", "foo", mag, "auto", "", true},
	}
	for _, c := range cases {
		got, err := resolveEngine(c.req, c.uri, c.pref)
		if (err != nil) != c.wantErr {
			t.Fatalf("%s: err=%v wantErr=%v", c.name, err, c.wantErr)
		}
		if got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
