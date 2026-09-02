package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/gofiber/fiber/v3"
)

// fakeTenancyUsecase adalah ITenancyUsecase in-memory. Cukup untuk menguji
// lapisan HTTP: binding body, penerjemahan error ke kode status, dan bentuk
// response. Aturan bisnisnya sendiri sudah punya test di usecase.
type fakeTenancyUsecase struct {
	users  map[int64]*domainTenancy.User
	nextID int64

	// forceErr, kalau diisi, dikembalikan oleh setiap method. Dipakai untuk
	// menguji pemetaan error tanpa harus mereproduksi kondisinya.
	forceErr error
}

func newFakeTenancyUsecase() *fakeTenancyUsecase {
	return &fakeTenancyUsecase{users: map[int64]*domainTenancy.User{}, nextID: 1}
}

func (f *fakeTenancyUsecase) CreateUser(_ context.Context, in domainTenancy.CreateUserInput) (*domainTenancy.User, error) {
	if f.forceErr != nil {
		return nil, f.forceErr
	}
	role := in.Role
	if role == "" {
		role = domainTenancy.RoleOperator
	}
	user := &domainTenancy.User{
		ID:           f.nextID,
		Username:     domainTenancy.NormalizeUsername(in.Username),
		PasswordHash: "$2a$10$contohhashyangtidakbolehbocorkeresponseapinya",
		DisplayName:  in.DisplayName,
		Role:         role,
		DeviceLimit:  in.DeviceLimit,
		Active:       true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	f.nextID++
	f.users[user.ID] = user
	return user, nil
}

func (f *fakeTenancyUsecase) UpdateUser(_ context.Context, id int64, in domainTenancy.UpdateUserInput) (*domainTenancy.User, error) {
	if f.forceErr != nil {
		return nil, f.forceErr
	}
	user, ok := f.users[id]
	if !ok {
		return nil, domainTenancy.ErrUserNotFound
	}
	if in.DisplayName != nil {
		user.DisplayName = *in.DisplayName
	}
	if in.Role != nil {
		user.Role = *in.Role
	}
	if in.DeviceLimit != nil {
		user.DeviceLimit = *in.DeviceLimit
	}
	if in.Active != nil {
		user.Active = *in.Active
	}
	return user, nil
}

func (f *fakeTenancyUsecase) DeleteUser(_ context.Context, id int64) error {
	if f.forceErr != nil {
		return f.forceErr
	}
	if _, ok := f.users[id]; !ok {
		return domainTenancy.ErrUserNotFound
	}
	delete(f.users, id)
	return nil
}

func (f *fakeTenancyUsecase) GetUser(_ context.Context, id int64) (*domainTenancy.User, error) {
	if f.forceErr != nil {
		return nil, f.forceErr
	}
	user, ok := f.users[id]
	if !ok {
		return nil, domainTenancy.ErrUserNotFound
	}
	return user, nil
}

func (f *fakeTenancyUsecase) ListUsers(_ context.Context) ([]*domainTenancy.User, error) {
	if f.forceErr != nil {
		return nil, f.forceErr
	}
	users := make([]*domainTenancy.User, 0, len(f.users))
	for _, user := range f.users {
		users = append(users, user)
	}
	return users, nil
}

func (f *fakeTenancyUsecase) Authenticate(context.Context, string, string) (*domainTenancy.Principal, error) {
	return nil, nil
}

func (f *fakeTenancyUsecase) BootstrapAdminsFromEnv(context.Context, []string) (int, error) {
	return 0, nil
}

var _ domainTenancy.ITenancyUsecase = (*fakeTenancyUsecase)(nil)

func newAdminUsersTestApp(t *testing.T) (*fiber.App, *fakeTenancyUsecase) {
	t.Helper()
	svc := newFakeTenancyUsecase()
	app := fiber.New()
	InitRestAdminUsers(app, svc)
	return app, svc
}

func TestAdminUsersCreateAndList(t *testing.T) {
	app, _ := newAdminUsersTestApp(t)

	res, body := doJSON(t, app, http.MethodPost, "/admin/users",
		`{"username":"operator1","password":"rahasia123","display_name":"Operator Satu","role":"operator","device_limit":2}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}

	var created struct {
		Code    string `json:"code"`
		Results struct {
			ID          int64  `json:"id"`
			Username    string `json:"username"`
			Role        string `json:"role"`
			DeviceLimit int    `json:"device_limit"`
			Active      bool   `json:"active"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, body)
	}
	if created.Code != "SUCCESS" {
		t.Fatalf("code = %q", created.Code)
	}
	if created.Results.Username != "operator1" || created.Results.Role != "operator" {
		t.Fatalf("results = %+v", created.Results)
	}
	if created.Results.DeviceLimit != 2 || !created.Results.Active {
		t.Fatalf("results = %+v", created.Results)
	}

	res, body = doJSON(t, app, http.MethodGet, "/admin/users", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	if !strings.Contains(body, "operator1") {
		t.Fatalf("daftar user tidak memuat user yang baru dibuat: %s", body)
	}
}

// TestAdminUsersNeverLeakPasswordHash adalah test terpenting di file ini.
//
// PasswordHash memang sudah bertag `json:"-"` di DTO dan userView dibangun
// eksplisit, tapi keduanya mudah rusak saat refactor — dan yang bocor kalau
// rusak adalah hash password. Test ini memeriksa BODY MENTAH-nya, bukan struct
// hasil unmarshal, supaya field bernama apa pun tetap tertangkap.
func TestAdminUsersNeverLeakPasswordHash(t *testing.T) {
	app, _ := newAdminUsersTestApp(t)

	_, createBody := doJSON(t, app, http.MethodPost, "/admin/users",
		`{"username":"operator1","password":"rahasia123"}`)
	_, listBody := doJSON(t, app, http.MethodGet, "/admin/users", "")
	_, getBody := doJSON(t, app, http.MethodGet, "/admin/users/1", "")
	_, patchBody := doJSON(t, app, http.MethodPatch, "/admin/users/1", `{"display_name":"Baru"}`)

	for name, body := range map[string]string{
		"create": createBody,
		"list":   listBody,
		"get":    getBody,
		"patch":  patchBody,
	} {
		if strings.Contains(body, "$2a$") || strings.Contains(body, "$2b$") {
			t.Fatalf("response %s memuat hash bcrypt: %s", name, body)
		}
		for _, field := range []string{"password_hash", "passwordHash", "PasswordHash", "password"} {
			if strings.Contains(body, field) {
				t.Fatalf("response %s memuat field %q: %s", name, field, body)
			}
		}
	}
}

func TestAdminUsersPatchOnlySendsGivenFields(t *testing.T) {
	app, svc := newAdminUsersTestApp(t)

	if _, err := svc.CreateUser(context.Background(), domainTenancy.CreateUserInput{
		Username:    "operator1",
		Password:    "rahasia123",
		Role:        domainTenancy.RoleOperator,
		DeviceLimit: 4,
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	res, body := doJSON(t, app, http.MethodPatch, "/admin/users/1", `{"display_name":"Operator Satu"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}

	// Field yang tidak dikirim tidak boleh berubah: role tetap operator,
	// device_limit tetap 4, dan user tetap aktif.
	stored := svc.users[1]
	if stored.Role != domainTenancy.RoleOperator {
		t.Fatalf("role = %q, want operator", stored.Role)
	}
	if stored.DeviceLimit != 4 {
		t.Fatalf("device limit = %d, want 4", stored.DeviceLimit)
	}
	if !stored.Active {
		t.Fatal("user tidak boleh ikut dinonaktifkan")
	}
	if stored.DisplayName != "Operator Satu" {
		t.Fatalf("display name = %q", stored.DisplayName)
	}
}

// TestAdminUsersPatchWarnsWhenSessionsRevoked: operator perlu diberi tahu bahwa
// perubahan ini menendang user dari semua perangkatnya.
func TestAdminUsersPatchWarnsWhenSessionsRevoked(t *testing.T) {
	app, svc := newAdminUsersTestApp(t)
	if _, err := svc.CreateUser(context.Background(), domainTenancy.CreateUserInput{Username: "operator1", Password: "rahasia123"}); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	_, body := doJSON(t, app, http.MethodPatch, "/admin/users/1", `{"password":"rahasiabaru1"}`)
	if !strings.Contains(body, "session") {
		t.Fatalf("response harus menyebutkan pencabutan session: %s", body)
	}

	_, body = doJSON(t, app, http.MethodPatch, "/admin/users/1", `{"active":false}`)
	if !strings.Contains(body, "session") {
		t.Fatalf("response harus menyebutkan pencabutan session: %s", body)
	}

	// Perubahan tak berbahaya tidak boleh mengklaim ada session yang dicabut.
	_, body = doJSON(t, app, http.MethodPatch, "/admin/users/1", `{"display_name":"X"}`)
	if strings.Contains(body, "session") {
		t.Fatalf("response tidak boleh menyebut session: %s", body)
	}
}

// TestAdminUsersErrorMapping memastikan error validasi tidak berubah menjadi
// 500. Ini alasan handler tidak memakai utils.PanicIfNeeded untuk error usecase.
func TestAdminUsersErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		method   string
		path     string
		body     string
		wantCode int
		wantSlug string
	}{
		{"username dipakai", domainTenancy.ErrUsernameTaken, http.MethodPost, "/admin/users", `{"username":"a","password":"b"}`, http.StatusBadRequest, "USERNAME_TAKEN"},
		{"username tidak valid", domainTenancy.ErrUsernameInvalid, http.MethodPost, "/admin/users", `{"username":"a","password":"b"}`, http.StatusBadRequest, "VALIDATION_ERROR"},
		{"role tidak valid", domainTenancy.ErrRoleInvalid, http.MethodPost, "/admin/users", `{"username":"a","password":"b"}`, http.StatusBadRequest, "VALIDATION_ERROR"},
		{"limit negatif", domainTenancy.ErrDeviceLimitInvalid, http.MethodPost, "/admin/users", `{"username":"a","password":"b"}`, http.StatusBadRequest, "VALIDATION_ERROR"},
		{"admin terakhir", domainTenancy.ErrLastAdminProtected, http.MethodDelete, "/admin/users/1", "", http.StatusBadRequest, "LAST_ADMIN_PROTECTED"},
		{"user tidak ada", domainTenancy.ErrUserNotFound, http.MethodGet, "/admin/users/1", "", http.StatusNotFound, "USER_NOT_FOUND"},
		{"error tak terduga", errors.New("disk meledak"), http.MethodGet, "/admin/users", "", http.StatusInternalServerError, "INTERNAL_SERVER_ERROR"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, svc := newAdminUsersTestApp(t)
			svc.forceErr = tc.err

			res, body := doJSON(t, app, tc.method, tc.path, tc.body)
			if res.StatusCode != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", res.StatusCode, tc.wantCode, body)
			}
			if !strings.Contains(body, tc.wantSlug) {
				t.Fatalf("body tidak memuat %q: %s", tc.wantSlug, body)
			}
		})
	}
}

func TestAdminUsersRejectsInvalidPathID(t *testing.T) {
	app, _ := newAdminUsersTestApp(t)

	for _, path := range []string{"/admin/users/abc", "/admin/users/0", "/admin/users/-1"} {
		res, body := doJSON(t, app, http.MethodGet, path, "")
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (body %s)", path, res.StatusCode, body)
		}
	}
}

func TestAdminUsersDeleteExplainsDeviceFate(t *testing.T) {
	app, svc := newAdminUsersTestApp(t)
	if _, err := svc.CreateUser(context.Background(), domainTenancy.CreateUserInput{Username: "operator1", Password: "rahasia123"}); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	res, body := doJSON(t, app, http.MethodDelete, "/admin/users/1", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	// Perilakunya benar tapi mengejutkan kalau tidak dijelaskan: device tidak
	// ikut terhapus, hanya kehilangan pemilik.
	if !strings.Contains(body, "pemilik") {
		t.Fatalf("response harus menjelaskan apa yang terjadi pada device: %s", body)
	}
	if _, ok := svc.users[1]; ok {
		t.Fatal("user harus terhapus")
	}
}
