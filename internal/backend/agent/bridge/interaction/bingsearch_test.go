package interaction

import (
	"encoding/base64"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"strings"
	"testing"
	"time"

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

type liveWebSearchCase struct {
	domain   string
	query    string
	needles  []string
	minHits  int
	wantCN   bool
	wantIntl bool
}

func TestLiveMultiDomainWebSearchOptional(t *testing.T) {
	if os.Getenv("WEBSEARCH_LIVE") != "1" {
		t.Skip("set WEBSEARCH_LIVE=1 to probe real Bing+Baidu merge")
	}
	bridge := NewBridge()
	cases := []liveWebSearchCase{
		{domain: "crypto", query: "okx", needles: []string{"okx.com", "okx"}, minHits: 1, wantCN: true, wantIntl: true},
		{domain: "infra", query: "kubernetes ingress nginx", needles: []string{"kubernetes", "nginx", "github.com"}, minHits: 1, wantIntl: true},
		{domain: "cn-policy", query: "个人所得税专项附加扣除", needles: []string{"chinatax", "gov.cn", "个税", "专项附加"}, minHits: 1, wantCN: true},
		{domain: "science", query: "CRISPR gene editing", needles: []string{"nih.gov", "nature.com", "wikipedia", "crispr", "broadinstitute"}, minHits: 1, wantIntl: true},
		{domain: "sports", query: "Premier League table", needles: []string{"premierleague", "skysports", "bbc", "espn"}, minHits: 1, wantIntl: true},
		{domain: "product", query: "Sony WH-1000XM5 review", needles: []string{"sony", "rtings", "whathifi", "wh-1000xm5"}, minHits: 1, wantIntl: true},
		{domain: "medical", query: "semaglutide Ozempic", needles: []string{"nih.gov", "fda.gov", "wikipedia", "novo", "ozempic", "semaglutide"}, minHits: 1, wantIntl: true},
		{domain: "academic", query: "Attention Is All You Need transformer", needles: []string{"arxiv.org", "neurips", "nips", "google"}, minHits: 1, wantIntl: true},
		{domain: "local-cn", query: "杭州西湖 开放时间", needles: []string{"hangzhou", "westlake", "西湖", "gov.cn", "trip"}, minHits: 1, wantCN: true},
		{domain: "markets", query: "NASDAQ AAPL stock", needles: []string{"apple.com", "nasdaq", "yahoo", "bloomberg", "aapl"}, minHits: 1, wantIntl: true},
		{domain: "news", query: "typhoon Pacific August 2026", needles: []string{"typhoon", "nhc", "jma", "reuters", "cnn", "weather"}, minHits: 1},
		{domain: "code", query: "golang context deadline exceeded", needles: []string{"go.dev", "pkg.go.dev", "stackoverflow", "github.com", "context"}, minHits: 1, wantIntl: true},
	}

	type row struct {
		domain     string
		query      string
		ms         int64
		n          int
		cn         int
		intl       int
		err        string
		needlesOK  bool
		titles     []string
		urls       []string
	}
	rows := make([]row, 0, len(cases))
	var totalMS int64
	var fail int
	for _, tc := range cases {
		started := time.Now()
		refs, _, err := bridge.executeWebSearch(tc.query)
		elapsed := time.Since(started).Milliseconds()
		totalMS += elapsed
		item := row{domain: tc.domain, query: tc.query, ms: elapsed, n: len(refs)}
		if err != nil {
			item.err = err.Error()
			fail++
			rows = append(rows, item)
			t.Logf("FAIL %s %q err=%v ms=%d", tc.domain, tc.query, err, elapsed)
			continue
		}
		needleHits := 0
		for _, ref := range refs {
			blob := strings.ToLower(ref.GetTitle() + " " + ref.GetUrl() + " " + ref.GetChunk())
			if isLikelyCNSearchURL(ref.GetUrl()) {
				item.cn++
			} else {
				item.intl++
			}
			for _, needle := range tc.needles {
				if strings.Contains(blob, strings.ToLower(needle)) {
					needleHits++
					break
				}
			}
			if len(item.titles) < 3 {
				item.titles = append(item.titles, ref.GetTitle())
				item.urls = append(item.urls, ref.GetUrl())
			}
		}
		item.needlesOK = needleHits >= tc.minHits
		if err == nil && item.n == 0 {
			fail++
		}
		if !item.needlesOK || (tc.wantCN && item.cn == 0) || (tc.wantIntl && item.intl == 0) {
			t.Logf("coverage weak [%s] needles=%v cn=%d intl=%d", tc.domain, item.needlesOK, item.cn, item.intl)
		}
		rows = append(rows, item)
	}

	t.Logf("==== merged WebSearch live bench n=%d avg=%dms total=%dms fail=%d ====", len(rows), totalMS/int64(len(rows)), totalMS, fail)
	for _, item := range rows {
		t.Logf("[%s] %q  %dms  n=%d cn=%d intl=%d needles=%v err=%s",
			item.domain, item.query, item.ms, item.n, item.cn, item.intl, item.needlesOK, item.err)
		for i := range item.titles {
			t.Logf("    %d. %s | %s", i+1, item.titles[i], item.urls[i])
		}
	}

	splitQueries := []string{"okx", "个人所得税专项附加扣除", "kubernetes ingress nginx"}
	for _, query := range splitQueries {
		bingStart := time.Now()
		bingRefs, _, bingErr := bridge.tryBingWebSearch(bridge.httpClient, query)
		bingMS := time.Since(bingStart).Milliseconds()
		baiduStart := time.Now()
		baiduRefs, _, baiduErr := bridge.tryBaiduWebSearch(bridge.httpClient, query)
		baiduMS := time.Since(baiduStart).Milliseconds()
		t.Logf("split %-24q bing=%dms n=%d err=%v | baidu=%dms n=%d err=%v | serial=%dms",
			query, bingMS, len(bingRefs), bingErr, baiduMS, len(baiduRefs), baiduErr, bingMS+baiduMS)
	}
	if fail > 0 {
		t.Fatalf("%d live cases missed expected coverage or returned errors", fail)
	}
}

func isLikelyCNSearchURL(rawURL string) bool {
	parsed, err := neturl.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return strings.Contains(strings.ToLower(rawURL), "baidu.com") || strings.Contains(strings.ToLower(rawURL), ".cn")
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "baidu.com" || strings.HasSuffix(host, ".baidu.com") ||
		strings.HasSuffix(host, ".cn") || strings.HasSuffix(host, ".com.cn")
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
