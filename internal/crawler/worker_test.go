package crawler

import (
	"testing"
)

// ---------- normalizeURL ----------

func TestNormalizeURL_Absolute(t *testing.T) {
	got, err := normalizeURL("https://Example.COM/Page", "https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example.com/Page" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeURL_Relative(t *testing.T) {
	got, err := normalizeURL("/about", "https://example.com/page")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example.com/about" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeURL_Fragment(t *testing.T) {
	got, err := normalizeURL("https://example.com/page#section", "https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example.com/page" {
		t.Errorf("fragment not stripped: got %q", got)
	}
}

func TestNormalizeURL_TrailingSlash(t *testing.T) {
	got, err := normalizeURL("https://example.com/page/", "https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example.com/page" {
		t.Errorf("trailing slash not stripped: got %q", got)
	}
}

func TestNormalizeURL_RootSlashKept(t *testing.T) {
	got, err := normalizeURL("https://example.com/", "https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example.com/" {
		t.Errorf("root slash should be kept: got %q", got)
	}
}

func TestNormalizeURL_RelativeDotDot(t *testing.T) {
	got, err := normalizeURL("../other", "https://example.com/a/b")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example.com/other" {
		t.Errorf("got %q", got)
	}
}

// ---------- isValidCrawlTarget ----------

func TestIsValid_HTTP(t *testing.T) {
	if !isValidCrawlTarget("http://example.com/page", false, "") {
		t.Error("http should be valid")
	}
	if !isValidCrawlTarget("https://example.com/page", false, "") {
		t.Error("https should be valid")
	}
}

func TestIsValid_RejectsMailto(t *testing.T) {
	if isValidCrawlTarget("mailto:user@example.com", false, "") {
		t.Error("mailto should be rejected")
	}
}

func TestIsValid_RejectsJavascript(t *testing.T) {
	if isValidCrawlTarget("javascript:void(0)", false, "") {
		t.Error("javascript should be rejected")
	}
}

func TestIsValid_RejectsBinaryExtensions(t *testing.T) {
	exts := []string{".pdf", ".jpg", ".png", ".zip", ".mp4", ".css", ".js"}
	for _, ext := range exts {
		u := "https://example.com/file" + ext
		if isValidCrawlTarget(u, false, "") {
			t.Errorf("should reject %s", ext)
		}
	}
}

func TestIsValid_SameDomain(t *testing.T) {
	if !isValidCrawlTarget("https://example.com/page", true, "example.com") {
		t.Error("same domain should be allowed")
	}
	if isValidCrawlTarget("https://other.com/page", true, "example.com") {
		t.Error("different domain should be rejected with sameDomain=true")
	}
}

func TestIsValid_SameDomainDisabled(t *testing.T) {
	if !isValidCrawlTarget("https://other.com/page", false, "example.com") {
		t.Error("different domain should be allowed when sameDomain=false")
	}
}

// ---------- parseHTML ----------

func TestParseHTML_Title(t *testing.T) {
	html := `<html><head><title>Test Title</title></head><body><p>Hello</p></body></html>`
	title, _, _ := parseHTML([]byte(html), "https://example.com")
	if title != "Test Title" {
		t.Errorf("title = %q, want %q", title, "Test Title")
	}
}

func TestParseHTML_Links(t *testing.T) {
	html := `<html><body>
		<a href="/page1">Link 1</a>
		<a href="https://other.com/page2">Link 2</a>
		<a href="#fragment">Link 3</a>
	</body></html>`
	_, links, _ := parseHTML([]byte(html), "https://example.com")
	if len(links) != 3 {
		t.Fatalf("got %d links, want 3", len(links))
	}
	if links[0] != "/page1" {
		t.Errorf("link[0] = %q", links[0])
	}
	if links[1] != "https://other.com/page2" {
		t.Errorf("link[1] = %q", links[1])
	}
}

func TestParseHTML_BodyText(t *testing.T) {
	html := `<html><body><p>Hello world</p><p>Go is great</p></body></html>`
	_, _, body := parseHTML([]byte(html), "https://example.com")
	if body == "" {
		t.Fatal("body should not be empty")
	}
	if !contains(body, "Hello world") || !contains(body, "Go is great") {
		t.Errorf("body missing expected text: %q", body)
	}
}

func TestParseHTML_SkipsScriptAndStyle(t *testing.T) {
	html := `<html><body><script>var x = 1;</script><style>body{}</style><p>Visible</p></body></html>`
	_, _, body := parseHTML([]byte(html), "https://example.com")
	if contains(body, "var x") {
		t.Error("script content should be skipped")
	}
	if contains(body, "body{}") {
		t.Error("style content should be skipped")
	}
	if !contains(body, "Visible") {
		t.Error("visible text should be included")
	}
}

func TestParseHTML_EmptyInput(t *testing.T) {
	title, links, body := parseHTML([]byte(""), "https://example.com")
	if title != "" {
		t.Errorf("title should be empty, got %q", title)
	}
	if len(links) != 0 {
		t.Errorf("links should be empty, got %d", len(links))
	}
	if body != "" {
		t.Errorf("body should be empty, got %q", body)
	}
}

func TestParseHTML_MalformedHTML(t *testing.T) {
	html := `<html><body><p>Unclosed <b>bold<p>Next`
	title, _, body := parseHTML([]byte(html), "https://example.com")
	_ = title // no crash is the test
	if body == "" {
		t.Error("should extract some body text from malformed HTML")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
