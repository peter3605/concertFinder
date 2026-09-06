package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/peterho/concertfinder/internal/db"
)

// RequireAdmin's three outcomes, including the one that only happens when
// somebody wires it up wrong.
func TestRequireAdmin(t *testing.T) {
	for _, tc := range []struct {
		name string
		// setUser is nil for a request that never went through RequireUser.
		setUser *db.User
		want    int
	}{
		{
			// The mis-mount: RequireAdmin outside RequireUser. It has to fail
			// closed, because the alternative is an ungated invite mint --
			// i.e. an unbounded signup path behind a gate that reads as
			// present.
			name: "no user on the context",
			want: http.StatusUnauthorized,
		},
		{
			name:    "signed in without the flag",
			setUser: &db.User{},
			want:    http.StatusForbidden,
		},
		{
			name:    "signed in with the flag",
			setUser: &db.User{IsAdmin: true},
			want:    http.StatusOK,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reached bool
			h := RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodGet, "/admin/invites", nil)
			if tc.setUser != nil {
				req = req.WithContext(withFullUser(req.Context(), *tc.setUser))
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if reached != (tc.want == http.StatusOK) {
				t.Fatalf("handler reached = %v, want %v", reached, tc.want == http.StatusOK)
			}
		})
	}
}
