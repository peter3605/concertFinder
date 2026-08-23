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
    // Regular width is iPad, and an iPhone in landscape on the larger
    // devices. Reviewers do open the iPad build, and a stretched four-tab
    // phone layout across 1024 points reads as an unported app.
    @Environment(\.horizontalSizeClass) private var sizeClass
    @State private var selection = Tab.feed

    enum Tab: Hashable, CaseIterable, Identifiable {
        case feed, saved, artists, settings

        var id: Self { self }

        var title: String {
            switch self {
            case .feed: "Concerts"
            case .saved: "Saved"
            case .artists: "Artists"
            case .settings: "Settings"
            }
        }

        var icon: String {
            switch self {
            case .feed: "music.note.list"
            case .saved: "bookmark"
            case .artists: "person.2"
            case .settings: "gearshape"
            }
        }
    }

    var body: some View {
        Group {
            if sizeClass == .regular {
                splitLayout
            } else {
                tabLayout
            }
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

    private var tabLayout: some View {
        TabView(selection: $selection) {
            ForEach(Tab.allCases) { tab in
                destination(for: tab)
                    .tabItem { Label(tab.title, systemImage: tab.icon) }
                    .tag(tab)
            }
        }
    }

    /// iPad and regular-width layout: a persistent sidebar rather than a
    /// bottom tab bar, which is the platform convention and stops the feed
    /// from stretching a phone-width card list across the full display.
    private var splitLayout: some View {
        NavigationSplitView {
            // List selection on iOS is an optional binding. The sidebar can
            // never actually be deselected here, so nil folds back to the
            // current tab rather than to an empty detail pane.
            List(Tab.allCases, selection: Binding(
                get: { Optional(selection) },
                set: { selection = $0 ?? selection }
            )) { tab in
                Label(tab.title, systemImage: tab.icon)
                    .tag(tab)
            }
            .navigationTitle("ConcertFinder")
            .listStyle(.sidebar)
        } detail: {
            destination(for: selection)
        }
    }

    @ViewBuilder
    private func destination(for tab: Tab) -> some View {
        switch tab {
        case .feed: FeedView()
        case .saved: SavedView()
        case .artists: ArtistsView()
        case .settings: SettingsView(api: api, baseURL: baseURL)
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
                    // A relative style rather than .system(size: 56): a fixed
                    // point size ignores Dynamic Type, so at the largest
                    // accessibility sizes the icon stays tiny while the text
                    // around it triples.
                    .font(.system(.largeTitle, design: .default))
                    .imageScale(.large)
                    .foregroundStyle(Color.accentColor)
                    .accessibilityHidden(true) // decorative; the title says it
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
