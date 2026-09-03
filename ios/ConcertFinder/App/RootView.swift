import SwiftUI
import UIKit

/// The app's top-level sections.
///
/// Top level rather than nested in `MainTabView` because the selection now
/// lives on `AppContainer`: a screen deep inside one tab needs to be able to
/// send the user to another (the empty feed's "get alerts" action points at
/// Artists), and a notification's deep link has to land on the feed.
enum AppTab: Hashable, CaseIterable, Identifiable {
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

    /// The idiom, not the size class. Reviewers do open the iPad build and a
    /// stretched four-tab phone layout across 1024 points reads as an
    /// unported app — but `horizontalSizeClass` is `.regular` for a
    /// Max-class iPhone in landscape too, so rotating the phone swapped the
    /// tab bar for a sidebar and tore down every navigation stack with it.
    private var isPad: Bool { UIDevice.current.userInterfaceIdiom == .pad }

    var body: some View {
        Group {
            if isPad {
                splitLayout
            } else {
                tabLayout
            }
        }
        // A tapped notification names an event, and the tap is a request to
        // see *that* card. Switching tabs was as far as this went, so the
        // notification opened a feed the user then had to scroll for.
        //
        // Clearing the key afterwards is the other half: `onChange` fires on a
        // change, so leaving the previous key in place made a second
        // notification about the same event a silent no-op.
        .onChange(of: container.pendingEventKey) { _, key in
            guard let key else { return }
            container.selectedTab = .feed
            Task {
                await feed.openEvent(withKey: key)
                container.pendingEventKey = nil
            }
        }
    }

    private var tabLayout: some View {
        @Bindable var container = container
        return TabView(selection: $container.selectedTab) {
            ForEach(AppTab.allCases) { tab in
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
            List(AppTab.allCases, selection: Binding(
                get: { Optional(container.selectedTab) },
                set: { container.selectedTab = $0 ?? container.selectedTab }
            )) { tab in
                Label(tab.title, systemImage: tab.icon)
                    .tag(tab)
            }
            .navigationTitle("ConcertFinder")
            .listStyle(.sidebar)
        } detail: {
            destination(for: container.selectedTab)
        }
    }

    @ViewBuilder
    private func destination(for tab: AppTab) -> some View {
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
            // Only when there is somewhere to send them. The screen is
            // deliberately undismissable, so a button that opens the App
            // Store to nothing is the last thing the user can do in the app.
            if let url = AppStoreLink.url() {
                Link("Open App Store", destination: url)
                    .buttonStyle(.borderedProminent)
            }
        }
    }
}

/// The App Store listing for this app, if there is one yet.
///
/// The app ID is assigned by App Store Connect on first submission, which has
/// not happened — so `CFAppStoreAppID` is an empty build setting today. It is
/// read rather than hardcoded so that filling it in is a one-line change in
/// `project.yml` with no Swift touched.
enum AppStoreLink {
    static func url(bundle: Bundle = .main) -> URL? {
        url(appID: bundle.object(forInfoDictionaryKey: "CFAppStoreAppID") as? String)
    }

    /// Split out so the gate is testable without a bundle.
    ///
    /// `allSatisfy(\.isNumber)` is not fussiness: an unset build setting can
    /// reach the plist as an empty string *or* as the literal
    /// `$(CF_APP_STORE_APP_ID)`, and both would otherwise build a URL that
    /// opens the App Store to nothing.
    static func url(appID: String?) -> URL? {
        guard let appID, !appID.isEmpty, appID.allSatisfy(\.isNumber) else { return nil }
        return URL(string: "itms-apps://apps.apple.com/app/id\(appID)")
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
