import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// HTTPS is required in dev because Spotify's redirect URI is
// https://127.0.0.1:3000/api/auth/callback and http://localhost is rejected
// as of Nov 2025. See docs/local-dev.md for mkcert setup.
const certPath = resolve(__dirname, 'certs/localhost-cert.pem');
const keyPath = resolve(__dirname, 'certs/localhost-key.pem');
const httpsAvailable = existsSync(certPath) && existsSync(keyPath);
if (!httpsAvailable) {
  // eslint-disable-next-line no-console
  console.warn(
    '[vite] HTTPS certs not found at web/certs/*.pem — running HTTP.\n' +
      '       Spotify auth will fail until you run mkcert (see docs/local-dev.md).',
  );
}

export default defineConfig({
  plugins: [react()],
  server: {
    port: 3000,
    host: '127.0.0.1',
    https: httpsAvailable
      ? { cert: readFileSync(certPath), key: readFileSync(keyPath) }
      : undefined,
    proxy: {
      // Everything under /api/ (including /api/auth/callback for the Spotify
      // OAuth redirect) is proxied to the Go backend unchanged. Backend
      // handlers are mounted at /api/*, so no rewrite is needed.
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
    },
  },
});
