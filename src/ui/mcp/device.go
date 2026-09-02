package mcp

import (
	"context"
	"errors"
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	mcpg "github.com/mark3labs/mcp-go/mcp"
)

// deviceResolver is the subset of *whatsapp.DeviceManager the MCP layer needs.
type deviceResolver interface {
	ResolveDevice(deviceID string) (*whatsapp.DeviceInstance, string, error)
}

// resolveDeviceContext returns a context carrying the device a tool call acts
// on. Priority: explicit device_id argument > device injected by the HTTP
// layer from the X-Device-Id header (see route.go) > error. Mirrors REST's
// DeviceMiddleware semantics for MCP handlers.
func resolveDeviceContext(ctx context.Context, request mcpg.CallToolRequest, resolver deviceResolver) (context.Context, *whatsapp.DeviceInstance, error) {
	// Kepemilikan ditegakkan DI DALAM resolver ini, bukan di setiap tool:
	// dengan begitu tool baru dari upstream otomatis terlindungi, alih-alih
	// lolos tanpa suara. Padanan pola yang dipakai resolver rute REST.
	if deviceID := strings.TrimSpace(request.GetString("device_id", "")); deviceID != "" {
		if resolver == nil {
			return ctx, nil, errors.New("device manager not initialized")
		}
		inst, resolvedID, err := resolver.ResolveDevice(deviceID)
		if err != nil {
			return ctx, nil, err
		}
		if err := enforceDeviceOwnership(ctx, resolvedID); err != nil {
			return ctx, nil, err
		}
		return whatsapp.ContextWithDevice(ctx, inst), inst, nil
	}

	if inst, ok := whatsapp.DeviceFromContext(ctx); ok && inst != nil {
		// Device dari header X-Device-Id sudah dijaga di route.go sebelum
		// masuk context, jadi di sini cukup dipakai.
		return ctx, inst, nil
	}

	// Tanpa device_id dan tanpa device di context: pakai device milik
	// pemanggil sendiri kalau pilihannya tunggal.
	ownID, err := ownDefaultDeviceID(ctx)
	if err != nil {
		return ctx, nil, err
	}
	if ownID != "" && resolver != nil {
		inst, resolvedID, err := resolver.ResolveDevice(ownID)
		if err != nil {
			return ctx, nil, deviceNotFound(ownID)
		}
		if err := enforceDeviceOwnership(ctx, resolvedID); err != nil {
			return ctx, nil, err
		}
		return whatsapp.ContextWithDevice(ctx, inst), inst, nil
	}

	return ctx, nil, errors.New("device identification required: set the X-Device-Id header or pass device_id")
}
