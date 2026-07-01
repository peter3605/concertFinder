import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// TODO: enable HTTPS via mkcert before wiring Spotify auth — the redirect URI
// (https://127.0.0.1:3000/callback) requires TLS even in dev.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 3000,
    host: '127.0.0.1',
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/api/, ''),
      },
    },
  },
});
