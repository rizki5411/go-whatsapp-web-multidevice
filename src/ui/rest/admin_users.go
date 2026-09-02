package rest

import (
	"errors"
	"fmt"
	"strconv"

	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/authhash"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

// Manajemen akun aplikasi untuk mode multi-tenant. Hanya didaftarkan saat
// config.MultiTenantEnabled aktif.
//
// Prefix /admin dipilih karena /user/* sudah dipakai untuk info user WhatsApp
// (user.go) dan menumpuk keduanya akan membingungkan.

// AdminUsersHandler menyajikan rute /admin/users*.
type AdminUsersHandler struct {
	Service domainTenancy.ITenancyUsecase
}

// InitRestAdminUsers mendaftarkan rute manajemen user.
//
// TODO(fase-03): bungkus grup ini dengan middleware.RequireAdmin(). Sampai
// fase 03 memasang auth gate, principal belum tersedia, sehingga rute ini masih
// terbuka bagi siapa pun yang punya kredensial APP_BASIC_AUTH. Itu sebabnya
// mode multi-tenant belum boleh dinyalakan di produksi setelah fase 02 —
// lihat docs/multitenant/README.md.
func InitRestAdminUsers(app fiber.Router, service domainTenancy.ITenancyUsecase) *AdminUsersHandler {
	h := &AdminUsersHandler{Service: service}

	app.Get("/admin/users", h.ListUsers)
	app.Post("/admin/users", h.CreateUser)
	app.Get("/admin/users/:id", h.GetUser)
	app.Patch("/admin/users/:id", h.UpdateUser)
	app.Delete("/admin/users/:id", h.DeleteUser)

	return h
}

// createUserRequest adalah body POST /admin/users.
type createUserRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	DeviceLimit int    `json:"device_limit"`
}

// updateUserRequest adalah body PATCH /admin/users/:id.
//
// Semua field pointer supaya field yang tidak dikirim berarti "jangan ubah".
// Tanpa itu, permintaan yang hanya mengganti nama tampilan akan mereset role
// dan mengaktifkan ulang user yang sengaja dinonaktifkan.
type updateUserRequest struct {
	Password    *string `json:"password"`
	DisplayName *string `json:"display_name"`
	Role        *string `json:"role"`
	DeviceLimit *int    `json:"device_limit"`
	Active      *bool   `json:"active"`
}

// userView adalah bentuk user di response.
//
// Dibangun eksplisit dan tidak pernah menyerahkan *tenancy.User langsung ke
// JSON. PasswordHash memang sudah bertag `json:"-"`, tapi tag itu mudah hilang
// saat refactor, dan yang bocor kalau hilang adalah hash password. Membangun
// view secara eksplisit membuat kebocoran itu mustahil, bukan cuma tidak
// disengaja.
func userView(user *domainTenancy.User) map[string]any {
	if user == nil {
		return nil
	}
	return map[string]any{
		"id":           user.ID,
		"username":     user.Username,
		"display_name": user.DisplayName,
		"role":         string(user.Role),
		"device_limit": user.DeviceLimit,
		"active":       user.Active,
		"created_at":   user.CreatedAt,
		"updated_at":   user.UpdatedAt,
	}
}

// respondTenancyError menerjemahkan error usecase ke kode HTTP dan kode
// aplikasi yang stabil.
//
// Error validasi sengaja TIDAK lewat utils.PanicIfNeeded: helper itu untuk
// kegagalan tak terduga, dan memakainya di sini akan mengubah "password terlalu
// pendek" menjadi 500.
func respondTenancyError(c fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, domainTenancy.ErrUserNotFound):
		return c.Status(fiber.StatusNotFound).JSON(utils.ResponseData{
			Status:  fiber.StatusNotFound,
			Code:    "USER_NOT_FOUND",
			Message: "user tidak ditemukan",
		})
	case errors.Is(err, domainTenancy.ErrUsernameTaken):
		return c.Status(fiber.StatusBadRequest).JSON(utils.ResponseData{
			Status:  fiber.StatusBadRequest,
			Code:    "USERNAME_TAKEN",
			Message: err.Error(),
		})
	case errors.Is(err, domainTenancy.ErrLastAdminProtected):
		return c.Status(fiber.StatusBadRequest).JSON(utils.ResponseData{
			Status:  fiber.StatusBadRequest,
			Code:    "LAST_ADMIN_PROTECTED",
			Message: err.Error(),
		})
	case errors.Is(err, domainTenancy.ErrUsernameInvalid),
		errors.Is(err, domainTenancy.ErrRoleInvalid),
		errors.Is(err, domainTenancy.ErrDeviceLimitInvalid),
		errors.Is(err, domainTenancy.ErrUserRequired),
		errors.Is(err, authhash.ErrPasswordTooShort),
		errors.Is(err, authhash.ErrPasswordTooLong):
		return c.Status(fiber.StatusBadRequest).JSON(utils.ResponseData{
			Status:  fiber.StatusBadRequest,
			Code:    "VALIDATION_ERROR",
			Message: err.Error(),
		})
	default:
		return c.Status(fiber.StatusInternalServerError).JSON(utils.ResponseData{
			Status:  fiber.StatusInternalServerError,
			Code:    "INTERNAL_SERVER_ERROR",
			Message: fmt.Sprintf("gagal memproses permintaan: %v", err),
		})
	}
}

// parseUserID membaca :id dari path.
func parseUserID(c fiber.Ctx) (int64, error) {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("id user tidak valid")
	}
	return id, nil
}

// ListUsers mengembalikan semua akun aplikasi.
// GET /admin/users
func (h *AdminUsersHandler) ListUsers(c fiber.Ctx) error {
	users, err := h.Service.ListUsers(c.Context())
	if err != nil {
		return respondTenancyError(c, err)
	}

	views := make([]map[string]any, 0, len(users))
	for _, user := range users {
		views = append(views, userView(user))
	}
	return c.JSON(utils.ResponseData{
		Status:  fiber.StatusOK,
		Code:    "SUCCESS",
		Message: "Daftar user",
		Results: views,
	})
}

// GetUser mengembalikan satu akun.
// GET /admin/users/:id
func (h *AdminUsersHandler) GetUser(c fiber.Ctx) error {
	id, err := parseUserID(c)
	if err != nil {
		return utils.ResponseError(c, err.Error())
	}

	user, err := h.Service.GetUser(c.Context(), id)
	if err != nil {
		return respondTenancyError(c, err)
	}
	return c.JSON(utils.ResponseData{
		Status:  fiber.StatusOK,
		Code:    "SUCCESS",
		Message: "Detail user",
		Results: userView(user),
	})
}

// CreateUser membuat akun baru.
// POST /admin/users
func (h *AdminUsersHandler) CreateUser(c fiber.Ctx) error {
	var req createUserRequest
	if err := c.Bind().Body(&req); err != nil {
		return utils.ResponseError(c, "Invalid request body")
	}

	user, err := h.Service.CreateUser(c.Context(), domainTenancy.CreateUserInput{
		Username:    req.Username,
		Password:    req.Password,
		DisplayName: req.DisplayName,
		Role:        domainTenancy.Role(req.Role),
		DeviceLimit: req.DeviceLimit,
	})
	if err != nil {
		return respondTenancyError(c, err)
	}

	return c.Status(fiber.StatusOK).JSON(utils.ResponseData{
		Status:  fiber.StatusOK,
		Code:    "SUCCESS",
		Message: "User dibuat",
		Results: userView(user),
	})
}

// UpdateUser mengubah field yang dikirim saja.
// PATCH /admin/users/:id
func (h *AdminUsersHandler) UpdateUser(c fiber.Ctx) error {
	id, err := parseUserID(c)
	if err != nil {
		return utils.ResponseError(c, err.Error())
	}

	var req updateUserRequest
	if err := c.Bind().Body(&req); err != nil {
		return utils.ResponseError(c, "Invalid request body")
	}

	in := domainTenancy.UpdateUserInput{
		Password:    req.Password,
		DisplayName: req.DisplayName,
		DeviceLimit: req.DeviceLimit,
		Active:      req.Active,
	}
	if req.Role != nil {
		role := domainTenancy.Role(*req.Role)
		in.Role = &role
	}

	user, err := h.Service.UpdateUser(c.Context(), id, in)
	if err != nil {
		return respondTenancyError(c, err)
	}

	message := "User diperbarui"
	// Operator perlu tahu konsekuensinya: kedua perubahan ini mencabut seluruh
	// session user tersebut, jadi ia akan tertendang dari semua perangkat.
	if req.Password != nil || (req.Active != nil && !*req.Active) {
		message = "User diperbarui; semua session-nya dicabut"
	}

	return c.JSON(utils.ResponseData{
		Status:  fiber.StatusOK,
		Code:    "SUCCESS",
		Message: message,
		Results: userView(user),
	})
}

// DeleteUser menghapus akun beserta session dan kepemilikan device-nya.
//
// Device-nya sendiri tidak dihapus: ia menjadi tak-ber-owner dan hanya terlihat
// oleh admin. Mem-purge device berarti memutus sesi WhatsApp-nya, dan itu harus
// tetap keputusan eksplisit.
// DELETE /admin/users/:id
func (h *AdminUsersHandler) DeleteUser(c fiber.Ctx) error {
	id, err := parseUserID(c)
	if err != nil {
		return utils.ResponseError(c, err.Error())
	}

	if err := h.Service.DeleteUser(c.Context(), id); err != nil {
		return respondTenancyError(c, err)
	}

	return c.JSON(utils.ResponseData{
		Status:  fiber.StatusOK,
		Code:    "SUCCESS",
		Message: "User dihapus; device miliknya menjadi tanpa pemilik dan hanya terlihat oleh admin",
	})
}
