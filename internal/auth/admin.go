package auth

import "net/http"

// RequireAdmin is the second authorization tier, and the only one. It refuses
// any request whose user does not carry users.is_admin (migration 0022).
//
// It reads the flag off the db.User that RequireUser already stashed on the
// context, so it costs no query: the session join selects is_admin, which is
// why internal/db's sessionUserColumns has to keep carrying it.
//
// MOUNT IT WITH r.Use ON THE ROUTE SUBTREE, NEVER PER-HANDLER. What sits
// behind this gate is an invite mint, so a route that misses it is not a
// slightly-too-open page -- it is an unauthenticated signup path, which is the
// exact hole migration 0021's invite gate was built to close. Per-handler
// mounting works right up until somebody adds the next route, and nothing
// fails when they forget. internal/http's AdminHandler.Mount installs this
// itself for that reason: the routes and the gate are registered by the same
// function, so there is no call site that can omit it.
//
// It must be mounted *inside* RequireUser, and the failure when it is not is
// a 401 rather than a pass: FullUserFromContext returns (zero, false) for a
// request that never resolved a session, and a zero db.User has IsAdmin
// false. So both the "not mounted under RequireUser" bug and the ordinary
// "not signed in" case fail closed, which is the only direction this is
// allowed to be wrong in.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := FullUserFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !u.IsAdmin {
			// 403, not 404. Hiding the route would be security by obscurity
			// over a URL that is in the client bundle anyway, and the web
			// console reads this exact status to tell an ordinary user "you
			// are signed in, this is just not yours" instead of showing them
			// a broken page.
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
