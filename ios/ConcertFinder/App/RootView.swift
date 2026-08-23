import SwiftUI

/// Decides which of the four top-level states the app is in.
struct RootView: View {
    @Environment(AuthController.self) private var auth
    @Environment(AppContainer.self) private var container

    var body: some View {
        switch auth.state {
        case .loading:
            // Deliberately bare. This resolves in the time one request takes,
            // and a branded splash that flashes is worse than nothing.
            ProgressView()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(Color.screenBackground)

        case .signedOut:
            LoginView()

        case .updateRequired:
            UpdateRequiredView()

        case .signedIn:
            MainTabView(api: container.api, baseURL: container.baseURL)
        }
    }
}

struct MainTabView: View {
    let api: APIClient
    let baseURL: URL

    @Environment(FeedModel.self) private var feed
    @Environment(AppContainer.self) private var container
    @State private var selection = Tab.feed

    enum Tab: Hashable {
        case feed, saved, artists, settings
    }

    var body: some View {
        TabView(selection: $selection) {
            FeedView()
                .tabItem { Label("Concerts", systemImage: "music.note.list") }
                .tag(Tab.feed)
            SavedView()
                .tabItem { Label("Saved", systemImage: "bookmark") }
                .tag(Tab.saved)
            ArtistsView()
                .tabItem { Label("Artists", systemImage: "person.2") }
                .tag(Tab.artists)
            SettingsView(api: api, baseURL: baseURL)
                .tabItem { Label("Settings", systemImage: "gearshape") }
                .tag(Tab.settings)
        }
        // A tapped notification names an event. Switching to the feed is the
        // honest minimum — the event may not be in the current filtered view,
        // and silently clearing the user's filters to reveal it would be a
        // surprising thing to do to their screen.
        .onChange(of: container.pendingEventKey) { _, key in
            guard key != nil else { return }
            selection = .feed
        }
    }
}

/// The server's minimum build is above this one.
///
/// Blocking on purpose: this is the escape hatch that makes a breaking API
/// change survivable, and it is worthless if the user can dismiss it into an
/// app that then fails in confusing ways.
struct UpdateRequiredView: View {
    var body: some View {
        ContentUnavailableView {
            Label("Update required", systemImage: "arrow.down.circle")
        } description: {
            Text("This version of ConcertFinder no longer works with our servers. Please update from the App Store to keep going.")
        } actions: {
            Link("Open App Store", destination: URL(string: "itms-apps://apple.com/app")!)
                .buttonStyle(.borderedProminent)
        }
    }
}

struct LoginView: View {
    @Environment(AuthController.self) private var auth
    @State private var isSigningIn = false

    var body: some View {
        VStack(spacing: Metrics.loose) {
            Spacer()

            VStack(spacing: Metrics.tight) {
                Image(systemName: "music.mic")
                    .font(.system(size: 56))
                    .foregroundStyle(Color.accentColor)
                Text("ConcertFinder")
                    .font(.largeTitle.weight(.bold))
                Text("Shows near you, from the artists you already listen to.")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
            }

            Spacer()

            VStack(spacing: Metrics.gutter) {
                Button {
                    isSigningIn = true
                    Task {
                        await auth.signIn()
                        isSigningIn = false
                    }
                } label: {
                    if isSigningIn {
                        ProgressView().frame(maxWidth: .infinity)
                    } else {
                        Text("Continue with Spotify").frame(maxWidth: .infinity)
                    }
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .disabled(isSigningIn)

                if let error = auth.authError {
                    // Development Mode gets the server's own wording — it is
                    // a configuration state the user cannot retry past, and
                    // "try again" would be wrong advice.
                    Text(error.userMessage)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                }

                Text("We never see your Spotify password, and your listening history is never stored.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)

                SpotifyAttribution()
            }
            .padding(.bottom, Metrics.loose)
        }
        .padding(Metrics.loose)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Color.screenBackground)
    }
}
