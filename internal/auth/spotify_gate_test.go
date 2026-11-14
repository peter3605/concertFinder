package auth

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// CF-32: nobody who did not sign in with Spotify may cause a Spotify request
// (docs/public-path.md §7). The guarantee is built in two halves, and these
// tests pin one each.
//
//  1. Structural. Every Spotify Web API method takes an access token, and
//     there are exactly two ways to get one: the login callback's code
//     exchange, which *is* the Spotify sign-in, and TokenService.AccessTokenFor.
//     TestOnlyTokenServiceIssuesSpotifyTokens walks the source to keep it so:
//     a new Spotify call site, or a new route to a token, fails it until
//     someone has looked at it.
//  2. Behavioural. AccessTokenFor refuses an account with no Spotify identity
//     without a network request (TestNoSpotifyTokenForNonSpotifyUser).
//
// Together they cover every handler and every worker, including ones not yet
// written, which is the point: a per-call-site test would only cover the
// call sites someone remembered.

// spotifyCallers is the complete set of non-test files outside the spotify
// package that call a *spotify.Client method, and how each one obtained the
// token it passes.
var spotifyCallers = map[string]string{
	"internal/auth/handlers.go":           "ExchangeCode", // the Spotify sign-in itself
	"internal/affinity/service.go":        "AccessTokenFor",
	"internal/http/subscribed_artists.go": "AccessTokenFor",
}

// tokenSources are the only functions allowed to obtain a Spotify access
// token, and the one file each may be called from.
var tokenSources = map[string]string{
	"ExchangeCode":       "internal/auth/handlers.go",
	"RefreshAccessToken": "internal/auth/access.go",
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root not found at %s: %v", root, err)
	}
	return root
}

// goFiles parses every non-test .go file under the given directories.
func goFiles(t *testing.T, root string, dirs ...string) map[string]*ast.File {
	t.Helper()
	out := map[string]*ast.File{}
	fset := token.NewFileSet()
	for _, d := range dirs {
		err := filepath.WalkDir(filepath.Join(root, d), func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			out[filepath.ToSlash(rel)] = f
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", d, err)
		}
	}
	return out
}

// calledNames returns every selector or identifier that appears as a callee.
func calledNames(f *ast.File) map[string]bool {
	names := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			names[fn.Sel.Name] = true
		case *ast.Ident:
			names[fn.Name] = true
		}
		return true
	})
	return names
}

func TestOnlyTokenServiceIssuesSpotifyTokens(t *testing.T) {
	root := repoRoot(t)

	// Every exported method on *spotify.Client, each of which must take an
	// access token -- that is what makes the token the chokepoint.
	methods := map[string]bool{}
	for path, f := range goFiles(t, root, "internal/spotify") {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() {
				continue
			}
			star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			if id, ok := star.X.(*ast.Ident); !ok || id.Name != "Client" {
				continue
			}
			takesToken := false
			for _, p := range fn.Type.Params.List {
				for _, n := range p.Names {
					if n.Name == "accessToken" {
						takesToken = true
					}
				}
			}
			if !takesToken {
				t.Errorf("%s: spotify.Client.%s takes no accessToken; every Spotify call must need one", path, fn.Name.Name)
			}
			methods[fn.Name.Name] = true
		}
	}
	if len(methods) == 0 {
		t.Fatal("found no spotify.Client methods; the walk is broken")
	}

	files := goFiles(t, root, "cmd", "internal")
	callers := map[string]bool{}
	for path, f := range files {
		if strings.HasPrefix(path, "internal/spotify/") {
			continue
		}
		called := calledNames(f)
		for m := range methods {
			if called[m] {
				callers[path] = true
			}
		}
		for src, home := range tokenSources {
			if called[src] && path != home {
				t.Errorf("%s calls %s; only %s may obtain a Spotify token that way", path, src, home)
			}
		}
	}

	var got []string
	for p := range callers {
		got = append(got, p)
		via, ok := spotifyCallers[p]
		if !ok {
			t.Errorf("%s calls a spotify.Client method but is not in spotifyCallers. "+
				"If its token comes from TokenService.AccessTokenFor it is gated (CF-32); add it here", p)
			continue
		}
		if !calledNames(files[p])[via] {
			t.Errorf("%s calls Spotify but not %s; where does its token come from?", p, via)
		}
	}
	sort.Strings(got)
	for p := range spotifyCallers {
		if !callers[p] {
			t.Errorf("spotifyCallers lists %s, which no longer calls Spotify; remove it (got %v)", p, got)
		}
	}
}

// countingTransport records every request and answers none.
type countingTransport struct {
	mu   sync.Mutex
	seen []string
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.seen = append(c.seen, r.URL.String())
	c.mu.Unlock()
	return nil, errors.New("no network in this test")
}

func TestNoSpotifyTokenForNonSpotifyUser(t *testing.T) {
	pool := identityTestPool(t)
	applyUpMigrations(t, pool, 1, allMigrations)
	ctx := context.Background()
	id := uuid.New()
	// A real-looking credential, so the only thing missing is the identity:
	// the refusal must not lean on the credential check that already existed.
	key := make([]byte, 32)
	ct, nonce, err := EncryptToken(key, []byte("refresh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce)
VALUES ($1, NULL, 'CF-32 no spotify', $2, $3)`, id, ct, nonce); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	rec := &countingTransport{}
	svc := &TokenService{Pool: pool, EncKey: key, ClientID: "test", HTTPClient: &http.Client{Transport: rec}}
	if _, err := svc.AccessTokenFor(ctx, id); !errors.Is(err, ErrSpotifyDisconnected) {
		t.Fatalf("AccessTokenFor = %v, want ErrSpotifyDisconnected", err)
	}
	if len(rec.seen) != 0 {
		t.Fatalf("AccessTokenFor contacted %v for a user with no Spotify identity", rec.seen)
	}
}
