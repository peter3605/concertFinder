import { lazy } from 'react';
import type React from 'react';
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom';
import { Layout } from '@/components/layout';
import { AuthProvider, useAuth } from '@/lib/auth';
import { ThemeProvider } from '@/lib/theme';
import ConcertsPage from '@/pages/concerts';
import LoginPage from '@/pages/login';
import NotFoundPage from '@/pages/not-found';
import SavedPage from '@/pages/saved';
import SettingsPage from '@/pages/settings';

// Split out of the main bundle. None of these is on the path to the first
// paint of the feed — the two policy pages are read once, the subscribe page
// pulls in its own search UI, /app/auth/callback exists for a browser that
// reached an iOS universal link, and the admin console has one user — so
// shipping them in the entry chunk is parse work every signed-in visit pays
// for nothing.
// Layout renders the Suspense boundary these need.
const AdminPage = lazy(() => import('@/pages/admin'));
const AppCallbackPage = lazy(() => import('@/pages/app-callback'));
const PrivacyPage = lazy(() => import('@/pages/privacy'));
const SubscribePage = lazy(() => import('@/pages/subscribe'));
const TermsPage = lazy(() => import('@/pages/terms'));

// RequireAuth gates authenticated routes. Anon users get bounced to /login;
// still-loading auth just renders nothing rather than flash the login screen.
function RequireAuth({ children }: { children: React.JSX.Element }) {
  const { auth } = useAuth();
  if (auth.kind === 'loading') return null;
  if (auth.kind === 'signed_in') return children;
  return <Navigate to="/login" replace />;
}

export default function App() {
  return (
    <ThemeProvider>
      <AuthProvider>
        <BrowserRouter>
          <Routes>
            <Route element={<Layout />}>
              <Route
                index
                element={
                  <RequireAuth>
                    <ConcertsPage />
                  </RequireAuth>
                }
              />
              <Route
                path="/saved"
                element={
                  <RequireAuth>
                    <SavedPage />
                  </RequireAuth>
                }
              />
              <Route
                path="/subscribe"
                element={
                  <RequireAuth>
                    <SubscribePage />
                  </RequireAuth>
                }
              />
              <Route
                path="/settings"
                element={
                  <RequireAuth>
                    <SettingsPage />
                  </RequireAuth>
                }
              />
              {/* The operator's console. Behind RequireAuth like any other
                  page, but nothing marks it as an admin route on the client:
                  the server decides, and the page renders "not yours" on the
                  403 it gets back. There is deliberately no nav link — adding
                  one would need the client to be told who is an admin, which
                  means an admin field in a payload iOS also decodes. */}
              <Route
                path="/admin"
                element={
                  <RequireAuth>
                    <AdminPage />
                  </RequireAuth>
                }
              />
              <Route path="/login" element={<LoginPage />} />
              {/* iOS universal link. Intercepted by the app when installed; this
                  route is what a browser reaching it sees instead of a silent
                  bounce to the feed. */}
              <Route path="/app/auth/callback" element={<AppCallbackPage />} />
              <Route path="/privacy" element={<PrivacyPage />} />
              <Route path="/terms" element={<TermsPage />} />
              {/* Not a redirect to `/`. Sending a dead link to the feed made
                  every typo look like a working page, and signed out it
                  bounced to the login screen as though that were the answer. */}
              <Route path="*" element={<NotFoundPage />} />
            </Route>
          </Routes>
        </BrowserRouter>
      </AuthProvider>
    </ThemeProvider>
  );
}
