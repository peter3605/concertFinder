// Tailwind 4 moved the PostCSS plugin into its own package; naming
// `tailwindcss` here is a hard error. Autoprefixer is gone with it — v4
// prefixes through Lightning CSS, and running both double-prefixes.
export default {
  plugins: {
    '@tailwindcss/postcss': {},
  },
};
