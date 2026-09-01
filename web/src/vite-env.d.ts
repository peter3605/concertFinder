/// <reference types="vite/client" />

// Vite's ambient types, which declare the asset-import modules — `import url
// from './x.svg'` resolves to a string only because of this reference. The
// project had no need for it until the Spotify logo became a real asset
// rather than a text placeholder; without the file, tsc fails the build with
// TS2307 while `vite build` alone would have succeeded, so the type error is
// the only thing that catches a mistyped asset path.
