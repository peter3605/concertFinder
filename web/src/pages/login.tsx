import { Music2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';

// Landing page for anonymous users. Explains what the app is + a single
// prominent CTA to start the Spotify OAuth flow.
export default function LoginPage() {
  return (
    <div className="mx-auto mt-16 max-w-md">
      <Card>
        <CardHeader className="items-center text-center">
          <div className="mb-2 inline-flex h-12 w-12 items-center justify-center rounded-full bg-primary/10 text-primary">
            <Music2 className="h-6 w-6" />
          </div>
          <CardTitle className="text-2xl">Welcome to ConcertFinder</CardTitle>
          <CardDescription>
            A personal concert feed built from your Spotify listening. Log in to
            start.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col items-center gap-3">
          <Button asChild size="lg" className="w-full">
            <a href="/api/auth/login">Log in with Spotify</a>
          </Button>
          <p className="text-center text-xs text-muted-foreground">
            We only read your library — never post, follow, or change anything.
            See <a href="/privacy" className="text-primary hover:underline">Privacy</a> for details.
          </p>
        </CardContent>
      </Card>
    </div>
  );
}
