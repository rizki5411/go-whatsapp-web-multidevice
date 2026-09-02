package tenancy

import "context"

// Pembawa identitas lewat context.Context.
//
// Dipakai jalur yang kehilangan fiber.Ctx di tengah perjalanan — MCP, yang
// melewati adaptor net/http sebelum sampai ke handler tool-nya. Jalur REST
// biasa memakai fiber Locals dan tidak perlu ini.

type principalContextKey struct{}

// ContextWithPrincipal menyisipkan identitas pemanggil ke context.
func ContextWithPrincipal(ctx context.Context, principal *Principal) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if principal == nil {
		return ctx
	}
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// PrincipalFromContext mengembalikan identitas pemanggil, atau nil kalau tidak
// ada.
//
// Pemanggil WAJIB menangani nil: context tanpa principal adalah keadaan normal
// di mode single-tenant dan di jalur yang memang publik.
func PrincipalFromContext(ctx context.Context) *Principal {
	if ctx == nil {
		return nil
	}
	if principal, ok := ctx.Value(principalContextKey{}).(*Principal); ok {
		return principal
	}
	return nil
}
