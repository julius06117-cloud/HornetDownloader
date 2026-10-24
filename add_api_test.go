package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testMagnet = "magnet:?xt=urn:btih:7ACF8FB590B2060DD9C3146EF770169D593433B0&dn=debian-13.7.0-amd64-netinst.iso"

type addCall struct{ eng, ref string }

func newTestApp(pref string, failBT error) (*App, *[]addCall) {
	calls := &[]addCall{}
	a := &App{cfg: Config{DownloadDir: "/tmp/dl", DefaultEngine: pref}, res: newResilience()}
	a.addURLHook = func(eng, uri, dir string, sel bool) (string, error) {
		*calls = append(*calls, addCall{eng, uri})
		if eng == EngineBT && failBT != nil {
			return "", failBT
		}
		return eng + "-gid", nil
	}
	a.addTorrentHook = func(eng, b64, name, dir string, sel bool) (string, error) {
		*calls = append(*calls, addCall{eng, name})
		if eng == EngineBT && failBT != nil {
			return "", failBT
		}
		return eng + "-tgid", nil
	}
	return a, calls
}

func postAdd(t *testing.T, a *App, body map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	rr := httptest.NewRecorder()
	a.handleAdd(rr, httptest.NewRequest(http.MethodPost, "/api/add", bytes.NewReader(b)))
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json: %s", rr.Body.String())
	}
	return out
}

func firstEngine(t *testing.T, out map[string]any) string {
	t.Helper()
	added, _ := out["added"].([]any)
	if len(added) == 0 {
		t.Fatalf("nothing added: %v", out)
	}
	return added[0].(map[string]any)["engine"].(string)
}

func TestAddAPIAutoUsesRecommendationDespiteOldDefault(t *testing.T) {
	a, calls := newTestApp("aria2", nil) // 旧版本保存的默认引擎
	out := postAdd(t, a, map[string]any{"engine": "auto", "urls": []string{testMagnet}})
	if got := firstEngine(t, out); got != EngineBT {
		t.Fatalf("auto magnet routed to %s, want bt (calls=%v)", got, *calls)
	}
}

func TestAddAPIManualChoiceHonored(t *testing.T) {
	for _, eng := range []string{EngineBT, EngineAria2} {
		a, _ := newTestApp("auto", nil)
		out := postAdd(t, a, map[string]any{"engine": eng, "urls": []string{testMagnet}})
		if got := firstEngine(t, out); got != eng {
			t.Fatalf("manual %s got %s", eng, got)
		}
	}
	a, _ := newTestApp("auto", nil)
	out := postAdd(t, a, map[string]any{"engine": "ytdlp", "urls": []string{"https://www.bilibili.com/video/BV1xx"}})
	if got := firstEngine(t, out); got != EngineYTDLP {
		t.Fatalf("manual ytdlp got %s", got)
	}
}

func TestAddAPIManualBTFailureDoesNotSilentlyUseAria2(t *testing.T) {
	a, calls := newTestApp("auto", errors.New("boom"))
	out := postAdd(t, a, map[string]any{"engine": "bt", "urls": []string{testMagnet}})
	if out["error"] == nil {
		t.Fatalf("expected error, got %v", out)
	}
	for _, c := range *calls {
		if c.eng == EngineAria2 {
			t.Fatal("manual BT must not fall back to aria2")
		}
	}
}

func TestAddAPIDuplicateDoesNotFallBack(t *testing.T) {
	// 根因回归：BT 已有同一 infohash 时，以前会静默回退 aria2，导致「总是 aria2」。
	// 现在应返回已有任务 id，绝不碰 aria2。
	a, calls := newTestApp("auto", &btDuplicateError{GID: "bt-1"})
	out := postAdd(t, a, map[string]any{"engine": "auto", "urls": []string{testMagnet}})
	if out["error"] != nil {
		t.Fatalf("duplicate should reuse existing task, got error: %v", out)
	}
	added := out["added"].([]any)[0].(map[string]any)
	if added["gid"] != "bt-1" || added["engine"] != EngineBT || added["existing"] != true {
		t.Fatalf("want existing bt-1, got %v", added)
	}
	for _, c := range *calls {
		if c.eng == EngineAria2 {
			t.Fatal("duplicate must not fall back to aria2")
		}
	}
}

func TestAddAPIAutoFallbackIsReported(t *testing.T) {
	a, _ := newTestApp("auto", errors.New("engine down"))
	out := postAdd(t, a, map[string]any{"engine": "auto", "urls": []string{testMagnet}})
	added := out["added"].([]any)[0].(map[string]any)
	if added["engine"] != EngineAria2 || added["fallback"] == nil || added["fallback"] == "" {
		t.Fatalf("fallback must be explicit: %v", added)
	}
}

func TestAddAPIDefaultPathsUseSettingsThenRecommendation(t *testing.T) {
	// 剪贴板/关联/拖放等入口发送 engine:""
	a, _ := newTestApp("auto", nil)
	if got := firstEngine(t, postAdd(t, a, map[string]any{"engine": "", "urls": []string{testMagnet}})); got != EngineBT {
		t.Fatalf("default+auto pref magnet -> %s", got)
	}
	a, _ = newTestApp("aria2", nil)
	if got := firstEngine(t, postAdd(t, a, map[string]any{"urls": []string{testMagnet}})); got != EngineAria2 {
		t.Fatalf("default with pref aria2 -> %s", got)
	}
}

func TestAddAPITorrentUploadRoutes(t *testing.T) {
	a, _ := newTestApp("aria2", nil)
	out := postAdd(t, a, map[string]any{"engine": "auto", "torrents": []map[string]string{{"name": "debian.torrent", "b64": "AA=="}}})
	if got := firstEngine(t, out); got != EngineBT {
		t.Fatalf("torrent upload auto -> %s", got)
	}
	a, _ = newTestApp("auto", nil)
	out = postAdd(t, a, map[string]any{"engine": "ytdlp", "torrents": []map[string]string{{"name": "x.torrent", "b64": "AA=="}}})
	if out["error"] == nil {
		t.Fatalf("ytdlp torrent must error: %v", out)
	}
}
