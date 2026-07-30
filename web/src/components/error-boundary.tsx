import { Component, type ReactNode } from 'react';

type State = { error: Error | null };

// Catches uncaught render/lifecycle errors so a single-component crash
// doesn't blank the entire app.
export default class ErrorBoundary extends Component<{ children: ReactNode }, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: unknown) {
    // eslint-disable-next-line no-console
    console.error('[ErrorBoundary] uncaught render error', error, info);
  }

  render() {
    if (this.state.error) {
      return (
        <div className="mx-auto max-w-lg p-8">
          <h1 className="text-xl font-semibold">Something went wrong</h1>
          <p className="mt-2 text-sm text-muted-foreground">
            The app hit an unexpected error. Try reloading the page.
          </p>
          <pre className="mt-4 whitespace-pre-wrap rounded-md border border-destructive/40 bg-destructive/5 p-3 text-xs text-destructive">
            {String(this.state.error?.message ?? this.state.error)}
          </pre>
          <p className="mt-4 text-sm">
            <a href="/" className="text-primary hover:underline">
              Back to home
            </a>
          </p>
        </div>
      );
    }
    return this.props.children;
  }
}
