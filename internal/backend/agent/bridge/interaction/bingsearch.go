package interaction

import (
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	neturl "net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"cursor/gen/agentv1"
)

const (
	bingWebSearchHostURL     = "https://www.bing.com"
	bingSearchReferenceLimit = 8
	bingSearchResultCap      = 5
)

type bingRSSFeed struct {
	Channel struct {
		Items []bingRSSItem `xml:"item"`
	} `xml:"channel"`
}

type bingRSSItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
}

// buildBingWebSearchURL 构造 Bing 国际版（www.bing.com + en-US market）搜索 URL。
// format=rss 时走 Bing 公开 RSS，避免 JS/验证码墙；空 format 则抓 HTML（SearXNG/OpenSERP 同款）。
func buildBingWebSearchURL(searchTerm string, rss bool) string {
	values := neturl.Values{}
	values.Set("q", searchTerm)
	values.Set("mkt", "en-US")
	values.Set("setlang", "en")
	values.Set("cc", "US")
	values.Set("adlt", "off")
	if rss {
		values.Set("format", "rss")
	}
	return bingWebSearchHostURL + "/search?" + values.Encode()
}

func (bridge *Bridge) tryBingWebSearch(client *http.Client, searchTerm string) ([]*agentv1.WebSearchReference, string, error) {
	references, err := bridge.fetchBingWebSearch(client, searchTerm, true)
	if err != nil || len(references) == 0 {
		htmlRefs, htmlErr := bridge.fetchBingWebSearch(client, searchTerm, false)
		if htmlErr == nil && len(htmlRefs) > 0 {
			references, err = htmlRefs, nil
		} else if err != nil {
			if htmlErr != nil {
				return nil, "", fmt.Errorf("bing rss=%v, html=%v", err, htmlErr)
			}
			return nil, "", err
		}
	}
	if len(references) == 0 {
		return nil, "", fmt.Errorf("bing returned no parseable results")
	}
	if len(references) > bingSearchResultCap {
		references = references[:bingSearchResultCap]
	}
	return references, formatWebSearchPayload(searchTerm, references), nil
}

func (bridge *Bridge) fetchBingWebSearch(client *http.Client, searchTerm string, rss bool) ([]*agentv1.WebSearchReference, error) {
	request, err := http.NewRequest(http.MethodGet, buildBingWebSearchURL(searchTerm, rss), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	if rss {
		request.Header.Set("Accept", "application/rss+xml, application/xml, text/xml, */*")
	} else {
		request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	}
	request.Header.Set("Accept-Language", "en-US,en;q=0.9")
	request.Header.Set("Referer", bingWebSearchHostURL+"/")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("bing http status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}
	text := string(body)
	if reason := bingSearchBlockReason(text); reason != "" {
		return nil, fmt.Errorf("%s", reason)
	}
	references := extractBingWebSearchReferences(text)
	if len(references) == 0 {
		return nil, fmt.Errorf("bing returned no parseable results")
	}
	return references, nil
}

func bingSearchBlockReason(body string) string {
	lower := strings.ToLower(body)
	if strings.Contains(lower, "<rss") {
		return ""
	}
	if strings.Contains(lower, "verify that you are not a robot") ||
		strings.Contains(lower, "enter the characters you see") ||
		strings.Contains(lower, "div class=\"captcha") {
		return "bing captcha"
	}
	return ""
}

func extractBingWebSearchReferences(body string) []*agentv1.WebSearchReference {
	trimmed := strings.TrimSpace(body)
	if strings.Contains(trimmed, "<rss") || strings.Contains(trimmed, "<RSS") {
		if refs := extractBingRSSReferences(trimmed); len(refs) > 0 {
			return refs
		}
	}
	return extractBingHTMLReferences(trimmed)
}

func extractBingRSSReferences(body string) []*agentv1.WebSearchReference {
	var feed bingRSSFeed
	if err := xml.Unmarshal([]byte(body), &feed); err != nil {
		return nil
	}
	references := make([]*agentv1.WebSearchReference, 0, bingSearchReferenceLimit)
	seen := map[string]struct{}{}
	for _, item := range feed.Channel.Items {
		if len(references) >= bingSearchReferenceLimit {
			break
		}
		if reference := newBingSearchReference(item.Title, item.Link, item.Description, seen); reference != nil {
			references = append(references, reference)
		}
	}
	return references
}

func extractBingHTMLReferences(body string) []*agentv1.WebSearchReference {
	document, err := goquery.NewDocumentFromReader(strings.NewReader(body))
	if err != nil {
		return nil
	}
	references := make([]*agentv1.WebSearchReference, 0, bingSearchReferenceLimit)
	seen := map[string]struct{}{}
	document.Find("#b_results > li.b_algo").EachWithBreak(func(_ int, item *goquery.Selection) bool {
		if len(references) >= bingSearchReferenceLimit {
			return false
		}
		anchor := item.Find("h2 a").First()
		if anchor.Length() == 0 {
			return true
		}
		title := strings.TrimSpace(anchor.AttrOr("aria-label", ""))
		if title == "" {
			title = cleanBaiduSearchText(anchor.Text())
		}
		snippet := cleanBaiduSearchText(item.Find("div.b_caption p").First().Text())
		if snippet == "" {
			snippet = cleanBaiduSearchText(item.Find("p").First().Text())
		}
		if reference := newBingSearchReference(title, anchor.AttrOr("href", ""), snippet, seen); reference != nil {
			references = append(references, reference)
		}
		return true
	})
	return references
}

func newBingSearchReference(title string, rawURL string, snippet string, seen map[string]struct{}) *agentv1.WebSearchReference {
	title = cleanBaiduSearchText(html.UnescapeString(title))
	resultURL := unwrapBingResultURL(rawURL)
	if title == "" || !isUsableBingResultURL(resultURL) {
		return nil
	}
	key := normalizeSearchURLKey(resultURL)
	if key == "" {
		return nil
	}
	if _, exists := seen[key]; exists {
		return nil
	}
	seen[key] = struct{}{}
	return &agentv1.WebSearchReference{
		Title: title,
		Url:   resultURL,
		Chunk: truncateBaiduSearchAbstract(cleanBaiduSearchText(html.UnescapeString(snippet))),
	}
}

func unwrapBingResultURL(rawURL string) string {
	rawURL = strings.TrimSpace(html.UnescapeString(rawURL))
	if rawURL == "" {
		return ""
	}
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	host := strings.ToLower(parsed.Hostname())
	if (host == "bing.com" || strings.HasSuffix(host, ".bing.com")) && strings.HasPrefix(parsed.EscapedPath(), "/ck/a") {
		encoded := parsed.Query().Get("u")
		if strings.HasPrefix(encoded, "a1") {
			payload := encoded[2:]
			if rem := len(payload) % 4; rem != 0 {
				payload += strings.Repeat("=", 4-rem)
			}
			decoded, decodeErr := base64.URLEncoding.DecodeString(payload)
			if decodeErr == nil {
				return strings.TrimSpace(string(decoded))
			}
		}
	}
	return rawURL
}

func isUsableBingResultURL(rawURL string) bool {
	parsed, err := neturl.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return false
	}
	if host == "bing.com" || strings.HasSuffix(host, ".bing.com") {
		return false
	}
	return true
}
