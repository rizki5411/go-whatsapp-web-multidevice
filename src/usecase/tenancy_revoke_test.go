package usecase

import (
	"context"
	"testing"

	domainTenancy "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/tenancy"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/websocket"
)

// Pencabutan koneksi WebSocket saat hak akun berubah.
//
// Koneksi WebSocket membawa salinan principal yang tidak pernah diperiksa ulang,
// jadi tanpa pencabutan ini "turunkan role" dan "nonaktifkan akun" berlaku
// seketika di HTTP tapi tidak sama sekali pada koneksi yang sudah terbuka.
// Kegagalannya senyap — tidak ada error, cuma event yang terus mengalir ke
// orang yang haknya sudah dicabut.

// captureRevocations mengganti channel Revoke dengan channel test dan
// mengembalikan pembaca isinya.
func captureRevocations(t *testing.T) func() []int64 {
	t.Helper()
	prev := websocket.Revoke
	channel := make(chan int64, 8)
	websocket.Revoke = channel
	t.Cleanup(func() { websocket.Revoke = prev })

	return func() []int64 {
		var got []int64
		for {
			select {
			case id := <-channel:
				got = append(got, id)
			default:
				return got
			}
		}
	}
}

func TestUpdateUserRevokesWebsocketOnRightsChange(t *testing.T) {
	cases := []struct {
		name  string
		input func(user *domainTenancy.User) domainTenancy.UpdateUserInput
	}{
		{"ganti password", func(*domainTenancy.User) domainTenancy.UpdateUserInput {
			password := "rahasiabaru1"
			return domainTenancy.UpdateUserInput{Password: &password}
		}},
		{"nonaktifkan akun", func(*domainTenancy.User) domainTenancy.UpdateUserInput {
			inactive := false
			return domainTenancy.UpdateUserInput{Active: &inactive}
		}},
		{"turunkan role", func(*domainTenancy.User) domainTenancy.UpdateUserInput {
			role := domainTenancy.RoleOperator
			return domainTenancy.UpdateUserInput{Role: &role}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newTenancyServiceForTest(t)
			revocations := captureRevocations(t)

			// Dua admin: penurunan role dan penonaktifan tidak boleh tertahan
			// invarian "admin terakhir".
			mustCreateUser(t, svc, "admin1", domainTenancy.RoleAdmin)
			user := mustCreateUser(t, svc, "admin2", domainTenancy.RoleAdmin)

			if _, err := svc.UpdateUser(context.Background(), user.ID, tc.input(user)); err != nil {
				t.Fatalf("update: %v", err)
			}

			got := revocations()
			if len(got) != 1 || got[0] != user.ID {
				t.Fatalf("pencabutan = %v, want [%d]", got, user.ID)
			}
		})
	}
}

// TestUpdateUserDoesNotRevokeWebsocketOnHarmlessChange: mengganti nama tampilan
// atau kuota device tidak mengubah hak apa pun, jadi tidak boleh memutus
// koneksi yang sedang dipakai.
func TestUpdateUserDoesNotRevokeWebsocketOnHarmlessChange(t *testing.T) {
	svc, _ := newTenancyServiceForTest(t)
	revocations := captureRevocations(t)

	user := mustCreateUser(t, svc, "operator1", domainTenancy.RoleOperator)

	name := "Nama Baru"
	limit := 5
	if _, err := svc.UpdateUser(context.Background(), user.ID, domainTenancy.UpdateUserInput{
		DisplayName: &name,
		DeviceLimit: &limit,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	// Role diset ke nilai yang SAMA juga bukan perubahan hak.
	role := domainTenancy.RoleOperator
	if _, err := svc.UpdateUser(context.Background(), user.ID, domainTenancy.UpdateUserInput{Role: &role}); err != nil {
		t.Fatalf("update role sama: %v", err)
	}

	if got := revocations(); len(got) != 0 {
		t.Fatalf("pencabutan = %v, want kosong", got)
	}
}

func TestDeleteUserRevokesWebsocket(t *testing.T) {
	svc, _ := newTenancyServiceForTest(t)
	revocations := captureRevocations(t)

	mustCreateUser(t, svc, "admin1", domainTenancy.RoleAdmin)
	user := mustCreateUser(t, svc, "operator1", domainTenancy.RoleOperator)

	if err := svc.DeleteUser(context.Background(), user.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	got := revocations()
	if len(got) != 1 || got[0] != user.ID {
		t.Fatalf("pencabutan = %v, want [%d]", got, user.ID)
	}
}

// TestChangeOwnPasswordDoesNotRevokeWebsocket mengunci keputusan yang disengaja:
// mengganti password sendiri tidak mengubah hak, dan ChangeOwnPassword memang
// dirancang untuk tidak menendang pengguna dari perangkat yang sedang
// dipakainya (pemanggilnya menerbitkan session baru).
func TestChangeOwnPasswordDoesNotRevokeWebsocket(t *testing.T) {
	svc, _ := newTenancyServiceForTest(t)
	revocations := captureRevocations(t)

	user := mustCreateUser(t, svc, "operator1", domainTenancy.RoleOperator)

	if err := svc.ChangeOwnPassword(context.Background(), user.ID, "rahasia123", "rahasiabaru1"); err != nil {
		t.Fatalf("change own password: %v", err)
	}

	if got := revocations(); len(got) != 0 {
		t.Fatalf("pencabutan = %v, want kosong", got)
	}
}
