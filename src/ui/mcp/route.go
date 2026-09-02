package mcp

import (
	"context"
	"net/http"
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sirupsen/logrus"
)

// Register mounts the MCP streamable-HTTP endpoint at /mcp on the given
// router (which already carries AppBasePath and the basic-auth middleware).
//
// Device scoping mirrors REST: the X-Device-Id header picks the device for
// the connection (empty resolves the default device, same as
// DeviceMiddleware); a per-call device_id tool argument overrides it (see
// resolveDeviceContext).
func Register(router fiber.Router, dm *whatsapp.DeviceManager, deps Deps) {
	// dm is typed here, but handlers take the deviceResolver interface;
	// a nil *DeviceManager must become a nil interface, not a typed nil.
	var resolver deviceResolver
	if dm != nil {
		resolver = dm
	}

	// Isolasi tenant (fitur fork). Dipasang di dalam Register supaya KEDUA
	// jalur mounting — OAuth (sebelum gate global) dan Basic (di belakangnya) —
	// otomatis terlindungi, alih-alih mengandalkan pemanggil mengingatnya.
	//
	// Use("/mcp", ...) dan bukan prefiks kosong: mounting pada prefiks kosong
	// ikut menangkap rute lain yang didaftarkan belakangan (lihat komentar
	// useMcpOAuthMiddleware di cmd/mcp_oauth.go).
	ownership = deps.Ownership
	if config.MultiTenantEnabled && deps.Tenancy != nil {
		router.Use("/mcp", PrincipalBridge(deps.Tenancy))
	}

	httpServer := server.NewStreamableHTTPServer(
		NewServer(deps, resolver),
		// Stateless: no server-initiated notifications or subscriptions are
		// used, and it avoids tying a session store to Fiber's shutdown.
		server.WithStateLess(true),
		// No server-initiated notifications are used, so the standalone GET
		// SSE stream is never needed. Without this, a GET reaches mcp-go's
		// "for { select { case <-writeChan: ...; case <-ctx.Done(): } }"
		// loop; ctx there is the *fasthttp.RequestCtx, whose Done() channel
		// only closes on server shutdown (not client disconnect), so the
		// goroutine, the fasthttp body-stream writer, and the connection/FD
		// would all be pinned forever through the fasthttp adaptor.
		server.WithDisableStreaming(true),
		server.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			// Principal dititipkan PrincipalBridge di fiber user context, dan
			// adaptor.HTTPHandlerWithContext membawanya sampai ke sini. Tanpa
			// pemindahan ini, handler tool tidak punya identitas apa pun.
			if fiberCtx, ok := adaptor.LocalContextFromHTTPRequest(r); ok {
				if principal := domainTenancy.PrincipalFromContext(fiberCtx); principal != nil {
					ctx = domainTenancy.ContextWithPrincipal(ctx, principal)
				}
			}

			if dm == nil {
				return ctx
			}
			deviceID := strings.TrimSpace(r.Header.Get(middleware.DeviceIDHeader))
			inst, resolvedID, err := dm.ResolveDevice(deviceID)
			if err != nil {
				// Leave the context empty; handlers surface a tool error
				// ("device identification required") on use.
				logrus.Debugf("MCP device resolution failed for %q: %v", deviceID, err)
				return ctx
			}
			// Device dari header hanya masuk context kalau pemanggil memang
			// memilikinya. Kalau tidak, context dibiarkan kosong dan
			// resolveDeviceContext akan memilih device pemanggil sendiri atau
			// menolak — jadi header device orang lain tidak pernah terpakai.
			if err := enforceDeviceOwnership(ctx, resolvedID); err != nil {
				logrus.Debugf("MCP device %q ditolak untuk pemanggil ini", resolvedID)
				return ctx
			}
			return whatsapp.ContextWithDevice(ctx, inst)
		}),
	)

	// HTTPHandlerWithContext, bukan HTTPHandler: hanya varian ini yang
	// menyimpan fiber user context ke request, dan itulah satu-satunya jalan
	// principal menyeberangi adaptor net/http.
	handler := adaptor.HTTPHandlerWithContext(httpServer)
	// POST carries JSON-RPC calls; DELETE is part of the streamable-HTTP
	// session lifecycle. GET is intentionally not mounted: with streaming
	// disabled mcp-go would just 405 it, so Fiber's own 404 for an
	// unmounted method is equivalent and keeps the route surface narrow.
	router.Post("/mcp", handler)
	router.Delete("/mcp", handler)
}
