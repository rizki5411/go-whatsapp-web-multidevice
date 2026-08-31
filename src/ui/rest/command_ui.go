package rest

import (
	_ "embed"

	"github.com/gofiber/fiber/v3"
)

// commandUIPage is the operator console for the "!" command system. It is
// embedded rather than downloaded because the main dashboard (gowa-ui) is a
// separate project whose released HTML is replaced on every auto-update, so a
// page for this fork's own feature has to ship with the binary.
//
// The page is self-contained (no external fonts, scripts, or styles): this
// server is often self-hosted without outbound internet access, and an admin
// page that needs a CDN to render is an admin page that fails when you most
// need it.
//
//go:embed assets/command_ui.html
var commandUIPage []byte

// CommandUI serves the command routing console.
// GET /command/ui
func (h *CommandConfigHandler) CommandUI(c fiber.Ctx) error {
	c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
	// The page reads live config; a cached copy would show stale routing.
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.Send(commandUIPage)
}
