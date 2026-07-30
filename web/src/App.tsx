import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom';
import { Layout } from '@/components/layout';
import { AuthProvider, useAuth } from '@/lib/auth';
import { ThemeProvider } from '@/lib/theme';
import ConcertsPage from '@/pages/concerts';
import LoginPage from '@/pages/login';
import PrivacyPage from '@/pages/privacy';
import SavedPage from '@/pages/saved';
import SettingsPage from '@/pages/settings';
import SubscribePage from '@/pages/subscribe';
import TermsPage from '@/pages/terms';

// RequireAuth gates authenticated routes. Anon users get bounced to /login;
// still-loading auth just renders nothing rather than flash the login screen.
function RequireAuth({ children }: { children: JSX.Element }) {
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
              <Route path="/login" element={<LoginPage />} />
              <Route path="/privacy" element={<PrivacyPage />} />
              <Route path="/terms" element={<TermsPage />} />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Route>
          </Routes>
        </BrowserRouter>
      </AuthProvider>
    </ThemeProvider>
  );
}
