package rest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/gofiber/fiber/v3"
)

func newCustomUITestApp() *fiber.App {
	app := fiber.New()
	InitRestCustomUI(app)
	return app
}

func getPage(t *testing.T, app *fiber.App, path string) (*http.Response, string) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil))
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	_ = resp.Body.Close()
	return resp, string(body)
}

func TestCustomUIPagesAreServed(t *testing.T) {
	app := newCustomUITestApp()

	tests := []struct {
		path  string
		title string
	}{
		{"/custom", "<title>Konsol Custom</title>"},
		{"/custom/command", "<title>Command Routing</title>"},
		{"/custom/queue", "<title>Antrian Pengiriman</title>"},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			resp, body := getPage(t, app, tc.path)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Fatalf("Content-Type = %q, want text/html", ct)
			}
			// Every console reads live state, so a cached copy would mislead.
			if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", cc)
			}
			if !strings.Contains(body, tc.title) {
				t.Fatalf("%s did not serve the expected page", tc.path)
			}
		})
	}
}

// The pages derive the API root by stripping their own path, so a route rename
// that misses the page's regex would silently break every call it makes.
func TestCustomUIPagesDeriveAPIRootFromTheirOwnPath(t *testing.T) {
	app := newCustomUITestApp()

	_, command := getPage(t, app, "/custom/command")
	if !strings.Contains(command, `replace(/\/custom\/command\/?$/, "")`) {
		t.Fatal("command console does not strip /custom/command to find the API root")
	}

	_, queue := getPage(t, app, "/custom/queue")
	if !strings.Contains(queue, `replace(/\/custom\/queue\/?$/, "")`) {
		t.Fatal("queue console does not strip /custom/queue to find the API root")
	}
}

// Pages must not pull anything off the network: this server is commonly
// self-hosted with no outbound access, and a console that needs a CDN fails
// exactly when it is needed.
func TestCustomUIPagesAreSelfContained(t *testing.T) {
	app := newCustomUITestApp()

	for _, path := range []string{"/custom", "/custom/command", "/custom/queue"} {
		_, body := getPage(t, app, path)
		for _, marker := range []string{"http://", "https://", "//cdn", "<script src=", "<link rel=\"stylesheet\""} {
			if strings.Contains(body, marker) {
				t.Fatalf("%s references external resource %q", path, marker)
			}
		}
	}
}

func TestLegacyCommandUIPathRedirects(t *testing.T) {
	app := newCustomUITestApp()

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/command/ui", nil))
	if err != nil {
		t.Fatalf("GET /command/ui: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/custom/command" {
		t.Fatalf("Location = %q, want /custom/command", loc)
	}
}

// The redirect has to carry APP_BASE_PATH, or it lands outside the mount point.
func TestLegacyCommandUIRedirectHonorsBasePath(t *testing.T) {
	original := config.AppBasePath
	config.AppBasePath = "/gowa"
	t.Cleanup(func() { config.AppBasePath = original })

	app := newCustomUITestApp()
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/command/ui", nil))
	if err != nil {
		t.Fatalf("GET /command/ui: %v", err)
	}
	defer resp.Body.Close()

	if loc := resp.Header.Get("Location"); loc != "/gowa/custom/command" {
		t.Fatalf("Location = %q, want /gowa/custom/command", loc)
	}
}
