import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// TODO: enable HTTPS via mkcert before wiring Spotify auth end-to-end — the
// redirect URI (https://127.0.0.1:3000/callback) requires TLS even in dev.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 3000,
    host: '127.0.0.1',
    proxy: {
      // SPA-facing API calls: /api/* → backend, with /api stripped.
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/api/, ''),
      },
      // Spotify redirects the browser here after consent; forward to the
      // backend's OAuth callback handler.
      '/callback': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
        rewrite: () => '/auth/callback',
      },
    },
  },
});
