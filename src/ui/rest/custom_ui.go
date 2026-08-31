package rest

import (
	_ "embed"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/gofiber/fiber/v3"
)

// Operator consoles for this fork's own features, served under /custom.
//
// They are embedded in the binary rather than added to the dashboard because the
// dashboard (gowa-ui) is a separate project downloaded into storages/ui at
// runtime and replaced on every auto-update, so anything edited there is lost.
//
// Every page is self-contained (no external fonts, scripts, or styles): this
// server is often self-hosted without outbound internet access, and an admin page
// that needs a CDN to render is an admin page that fails when you most need it.
//
// To add a console: drop the HTML in assets/, embed it, and register it as
// /custom/<name> in InitRestCustomUI.

//go:embed assets/custom_index.html
var customIndexPage []byte

//go:embed assets/command_ui.html
var commandUIPage []byte

//go:embed assets/queue_ui.html
var queueUIPage []byte

// CustomUIHandler serves the embedded operator consoles. It holds no state: the
// pages read everything they need from the REST API at runtime.
type CustomUIHandler struct{}

// InitRestCustomUI registers the /custom console routes.
func InitRestCustomUI(app fiber.Router) *CustomUIHandler {
	h := &CustomUIHandler{}

	app.Get("/custom", h.Index)
	app.Get("/custom/command", h.CommandUI)
	app.Get("/custom/queue", h.QueueUI)

	// The command console used to live at /command/ui. Redirect rather than drop
	// it, so an operator's bookmark does not turn into a mystery 404.
	app.Get("/command/ui", func(c fiber.Ctx) error {
		return c.Redirect().Status(fiber.StatusMovedPermanently).To(config.AppBasePath + "/custom/command")
	})

	return h
}

// Index lists the available consoles.
// GET /custom
func (h *CustomUIHandler) Index(c fiber.Ctx) error {
	return sendConsolePage(c, customIndexPage)
}

// CommandUI serves the "!" command routing console.
// GET /custom/command
func (h *CustomUIHandler) CommandUI(c fiber.Ctx) error {
	return sendConsolePage(c, commandUIPage)
}

// QueueUI serves the per-device send queue console.
// GET /custom/queue
func (h *CustomUIHandler) QueueUI(c fiber.Ctx) error {
	return sendConsolePage(c, queueUIPage)
}

// sendConsolePage writes an embedded page with caching off, because every console
// reads live state and a cached copy would show stale config or a stale queue.
func sendConsolePage(c fiber.Ctx, page []byte) error {
	c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.Send(page)
}
