package interaction

import (
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"cursor/gen/agentv1"
)

const bingRSS = `<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0"><channel>
<title>Bing: golang context</title>
<item><title>The Go Programming Language</title><link>https://go.dev/</link><description>Official Go site.</description></item>
<item><title>context package</title><link>https://pkg.go.dev/context</link><description>Package context defines Context.</description></item>
<item><title>Bing itself</title><link>https://www.bing.com/search?q=go</link><description>should skip</description></item>
</channel></rss>`

const bingHTML = `<html><body>
<ol id="b_results">
  <li class="b_algo">
    <h2><a href="https://go.dev/doc/">Go Documentation</a></h2>
    <div class="b_caption"><p>Official docs and tutorials.</p></div>
  </li>
  <li class="b_algo">
    <h2><a href="https://www.bing.com/ck/a?u=a1aHR0cHM6Ly9lbi53aWtpcGVkaWEub3JnL3dpa2kvR29fKHByb2dyYW1taW5nX2xhbmd1YWdlKQ">Wikipedia Go</a></h2>
    <div class="b_caption"><p>Go is a programming language.</p></div>
  </li>
  <li class="b_ad">
    <h2><a href="https://ads.example/go">Ad</a></h2>
  </li>
</ol>
</body></html>`

func TestExtractBingRSS(t *testing.T) {
	refs := extractBingWebSearchReferences(bingRSS)
	if len(refs) != 2 {
		t.Fatalf("got %d refs: %#v", len(refs), refs)
	}
	if refs[0].GetUrl() != "https://go.dev/" || refs[1].GetUrl() != "https://pkg.go.dev/context" {
		t.Fatalf("urls = %#v", refs)
	}
}

func TestExtractBingHTMLAndCKUnwrap(t *testing.T) {
	refs := extractBingWebSearchReferences(bingHTML)
	if len(refs) != 2 {
		t.Fatalf("got %d refs: %#v", len(refs), refs)
	}
	if refs[0].GetUrl() != "https://go.dev/doc/" {
		t.Fatalf("first = %q", refs[0].GetUrl())
	}
	if refs[1].GetUrl() != "https://en.wikipedia.org/wiki/Go_(programming_language)" {
		t.Fatalf("unwrapped = %q", refs[1].GetUrl())
	}
}

func TestUnwrapBingCKLink(t *testing.T) {
	raw := "a1" + strings.TrimRight(base64.URLEncoding.EncodeToString([]byte("https://example.com/a")), "=")
	got := unwrapBingResultURL("https://www.bing.com/ck/a?u=" + raw)
	if got != "https://example.com/a" {
		t.Fatalf("got %q", got)
	}
}

func TestMergeWebSearchReferencesBingFirst(t *testing.T) {
	bingRefs := []*agentv1.WebSearchReference{
		{Title: "Go", Url: "https://go.dev/", Chunk: "official"},
		{Title: "Video A", Url: "https://www.youtube.com/watch?v=aaa"},
	}
	baiduRefs := []*agentv1.WebSearchReference{
		{Title: "Go dup", Url: "https://go.dev/?utm=baidu"},
		{Title: "Video B", Url: "https://www.youtube.com/watch?v=bbb"},
		{Title: "CN", Url: "https://golang.google.cn/doc/"},
	}
	merged := mergeWebSearchReferences(bingRefs, baiduRefs, 8)
	if len(merged) != 4 ||
		merged[0].GetUrl() != "https://go.dev/" ||
		merged[1].GetUrl() != "https://www.youtube.com/watch?v=aaa" ||
		merged[2].GetUrl() != "https://www.youtube.com/watch?v=bbb" ||
		merged[3].GetUrl() != "https://golang.google.cn/doc/" {
		t.Fatalf("merged = %#v", merged)
	}
}

func TestIsUsableBingResultURLKeepsMicrosoftDocs(t *testing.T) {
	if !isUsableBingResultURL("https://learn.microsoft.com/en-us/dotnet/") {
		t.Fatal("learn.microsoft.com should be kept")
	}
	if isUsableBingResultURL("https://www.bing.com/search?q=go") {
		t.Fatal("bing.com should be skipped")
	}
}

func TestExecuteWebSearchMergesBingAndBaidu(t *testing.T) {
	bridge := &Bridge{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := ""
		switch {
		case strings.Contains(req.URL.Host, "bing."):
			if req.URL.Query().Get("mkt") != "en-US" {
				t.Errorf("expected international mkt=en-US, got %q", req.URL.Query().Get("mkt"))
			}
			if req.URL.Query().Get("format") == "rss" {
				body = bingRSS
			} else {
				body = bingHTML
			}
		case strings.Contains(req.URL.Host, "baidu."):
			body = baiduSearchHTML
		default:
			t.Fatalf("unexpected host %s", req.URL.Host)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}}

	refs, payload, err := bridge.executeWebSearch("golang context")
	if err != nil {
		t.Fatal(err)
	}
	if refs[0].GetUrl() != "https://go.dev/" {
		t.Fatalf("bing should lead, got %#v", refs)
	}
	foundBaidu := false
	for _, ref := range refs {
		if strings.Contains(ref.GetUrl(), "baidu-result.example") {
			foundBaidu = true
		}
	}
	if !foundBaidu {
		t.Fatalf("expected baidu unique result in %#v", refs)
	}
	if !strings.Contains(payload, "golang context") {
		t.Fatalf("payload = %s", payload)
	}
}

func TestExecuteWebSearchFallsBackWhenBingBlocked(t *testing.T) {
	bridge := &Bridge{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `<html><div class="captcha">verify that you are not a robot</div></html>`
		if strings.Contains(req.URL.Host, "baidu.") {
			body = baiduSearchHTML
		}
		if strings.Contains(req.URL.Host, "duckduckgo.") {
			t.Fatal("should not hit ddg when baidu works")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}}

	refs, _, err := bridge.executeWebSearch("golang context")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) == 0 || !strings.Contains(refs[0].GetUrl(), "baidu-result.example") {
		t.Fatalf("expected baidu fallback, got %#v", refs)
	}
}

func TestLiveBingWebSearchOptional(t *testing.T) {
	if os.Getenv("WEBSEARCH_LIVE") != "1" {
		t.Skip("set WEBSEARCH_LIVE=1 to probe real Bing")
	}
	bridge := NewBridge()
	refs, payload, err := bridge.tryBingWebSearch(bridge.httpClient, "golang context timeout")
	t.Logf("err=%v n=%d payload=%s", err, len(refs), truncateForLog(payload, 500))
	if err != nil {
		t.Fatalf("live bing failed: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("live bing returned empty refs")
	}
}

func truncateForLog(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

const baiduSearchHTML = `<html><body>
<div id="content_left">
  <div class="c-container">
    <h3><a href="https://baidu-result.example/go">百度 Go 文档</a></h3>
    <div class="c-abstract">中文镜像与入门说明</div>
  </div>
</div>
</body></html>`

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
