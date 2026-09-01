import SwiftUI
import UIKit

@main
struct ConcertFinderApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @State private var container = AppContainer()

    var body: some Scene {
        WindowGroup {
            RootView()
                // nil for `system`, which is how SwiftUI spells "do not
                // override the device".
                .preferredColorScheme(container.theme.choice.colorScheme)
                .environment(container.theme)
                .environment(container.auth)
                .environment(container.feed)
                .environment(container.saved)
                .environment(container.artists)
                .environment(container.location)
                .environment(container.discover)
                .environment(container.push)
                .environment(container)
                .task {
                    appDelegate.container = container
                    await container.start()
                }
                // Universal links. The OAuth return is handled inside
                // ASWebAuthenticationSession, so anything arriving here is a
                // link opened from outside the app.
                .onOpenURL { url in container.handle(url: url) }
        }
    }
}

/// Wires the object graph once and holds it for the app's lifetime.
///
/// Constructed by hand rather than through a DI framework: there are six
/// objects and one wiring point, and a container that can be read top to
/// bottom is worth more here than indirection.
@MainActor
@Observable
final class AppContainer {
    /// Appearance choice. Held here rather than in a view so the override is
    /// applied once, above every screen, instead of per-screen.
    let theme = ThemeStore()
    let api: APIClient
    let baseURL: URL
    let auth: AuthController
    let feed: FeedModel
    let saved: SavedModel
    let artists: ArtistsModel
    let location: LocationModel
    let discover: DiscoverModel
    let push: PushRegistrar

    /// Set when a notification or universal link asks for a specific event.
    var pendingEventKey: String?

    /// Which tab is showing. Here rather than in `MainTabView` because
    /// screens inside one tab now send the user to another — the empty feed
    /// points at Artists, a notification points at the feed — and view state
    /// is not reachable from either.
    var selectedTab: AppTab = .feed

    private let tokens = KeychainTokenStore()

    /// Held for the app's lifetime on purpose. This used to be constructed
    /// inline in `start()`, where nothing retained it: it was deallocated
    /// before the first request, so a 401 cleared the Keychain and left
    /// `auth.state` on `.signedIn` forever — the user kept a signed-in shell
    /// over an API that would answer nothing.
    private let invalidationBridge: SessionInvalidationBridge

    init() {
        let baseURL = Self.resolveBaseURL()
        let api = APIClient(baseURL: baseURL, tokens: tokens)
        self.api = api
        self.baseURL = baseURL
        let auth = AuthController(api: api, tokens: tokens, baseURL: baseURL)
        self.auth = auth
        self.feed = FeedModel(api: api)
        self.saved = SavedModel(api: api)
        self.artists = ArtistsModel(api: api)
        self.location = LocationModel(api: api)
        self.discover = DiscoverModel(api: api)
        self.push = PushRegistrar(api: api)
        // `[weak auth]` is what keeps this from being a cycle: the client
        // holds the bridge, the bridge would hold the controller, and the
        // controller holds the client.
        self.invalidationBridge = SessionInvalidationBridge { [weak auth] in
            auth?.handleSessionExpiry()
        }
    }

    func start() async {
        await seedUITestSessionIfRequested()
        // One place decides what a 401 means, so no screen has to.
        await api.setInvalidationHandler(invalidationBridge)
        self.auth.onSignIn = { [weak self] _ in
            Task { await self?.push.refreshRegistrationIfAuthorized() }
        }
        // Everything that must be undone when the session ends, by either
        // route. Assigned here rather than in Settings because expiry is the
        // path nobody taps: before this, a 401 left the previous account's
        // feed on screen and its device still registered for push.
        self.auth.onSignOut = { [weak self] in
            guard let self else { return }
            // Synchronous first. The sign-out has already flipped the UI, and
            // a frame of the previous account's concerts is the whole defect.
            self.feed.reset()
            self.saved.reset()
            self.artists.reset()
            self.location.reset()
            self.discover.reset()
            self.selectedTab = .feed
            // Awaited, and last: on the deliberate sign-out path this runs
            // while the session token is still valid, which is the only
            // moment the server will honour a deregistration.
            await self.push.deregister()
        }
        self.location.onLocationChanged = { [weak self] in
            guard let self else { return }
            Task {
                // The banner's own state first, then the results it describes
                // — otherwise "set your location" outlives the location the
                // user just set.
                await self.feed.refreshLocationState()
                await self.feed.load()
            }
        }
        await self.auth.restore()
        if case .signedIn = self.auth.state {
            // APNs rotates tokens, so re-register on every launch rather
            // than only after the first grant.
            await push.refreshRegistrationIfAuthorized()
        }
    }

    func handle(url: URL) {
        guard let components = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return }
        if let eventKey = components.queryItems?.first(where: { $0.name == "event_key" })?.value {
            pendingEventKey = eventKey
        }
    }

    func handle(notification userInfo: [AnyHashable: Any]) {
        guard let link = PushDeepLink(userInfo: userInfo) else { return }
        pendingEventKey = link.eventKey
    }

    /// Seeds a session handed in by the UI tests.
    ///
    /// The OAuth handshake runs in ASWebAuthenticationSession against
    /// Spotify's own login page, so it cannot be driven from a UI test
    /// without automating a third party's web UI and storing their
    /// credentials. Injecting an already-obtained session is the honest way
    /// to test everything *after* login.
    ///
    /// `#if DEBUG` is load-bearing: this must not exist in a Release binary,
    /// where an environment variable that installs a session would be a real
    /// vulnerability rather than a test affordance. App Store builds are
    /// Release, so the compiler removes it entirely.
    private func seedUITestSessionIfRequested() async {
        #if DEBUG
        let environment = ProcessInfo.processInfo.environment
        guard let token = environment["CF_UI_TEST_SESSION_TOKEN"], !token.isEmpty else { return }
        await tokens.store(token)
        #endif
    }

    /// The API origin comes from build configuration, so a debug build can
    /// point at a local server without a code change.
    private static func resolveBaseURL() -> URL {
        // Same DEBUG-only reasoning as the session seed above: a Release
        // binary must not let an environment variable redirect every request
        // to another host.
        #if DEBUG
        if let override = ProcessInfo.processInfo.environment["CF_UI_TEST_API_BASE_URL"],
           !override.isEmpty, let url = URL(string: override) {
            return url
        }
        #endif
        if let configured = Bundle.main.object(forInfoDictionaryKey: "CFAPIBaseURL") as? String,
           !configured.isEmpty,
           let url = URL(string: configured) {
            return url
        }
        // A build with no configured origin is a packaging mistake, but
        // crashing on launch is a worse way to find out than a login that
        // fails with a legible error.
        return URL(string: "https://127.0.0.1:3000")!
    }
}

/// Bridges APIClient's `SessionInvalidationHandler` to a closure, so the
/// networking layer does not have to know about AuthController.
private final class SessionInvalidationBridge: SessionInvalidationHandler, @unchecked Sendable {
    private let onExpiry: @MainActor () -> Void

    init(_ onExpiry: @escaping @MainActor () -> Void) {
        self.onExpiry = onExpiry
    }

    func sessionDidExpire() async {
        await MainActor.run { onExpiry() }
    }
}

/// Only exists for the two APNs callbacks, which have no SwiftUI equivalent.
final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    @MainActor var container: AppContainer?

    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions options: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        return true
    }

    func application(
        _ application: UIApplication,
        didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data
    ) {
        Task { @MainActor in
            await container?.push.didRegister(tokenData: deviceToken)
        }
    }

    func application(
        _ application: UIApplication,
        didFailToRegisterForRemoteNotificationsWithError error: Error
    ) {
        // Nothing to do and nothing to tell the user: the toggle stays on,
        // the next launch retries, and the only cost is notifications until
        // then.
    }

    /// A tapped notification deep-links into the event it names.
    ///
    /// nonisolated because UIApplicationDelegate conformance makes this class
    /// main-actor isolated, and UNNotificationResponse is not Sendable — so
    /// the parameter cannot cross the boundary. The userInfo dictionary is
    /// extracted here and only that hops to the main actor.
    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse
    ) async {
        let userInfo = response.notification.request.content.userInfo
        let keys = PushDeepLink(userInfo: userInfo)
        await MainActor.run { [weak self] in
            guard let keys else { return }
            self?.container?.pendingEventKey = keys.eventKey
        }
    }

    /// Show banners while the app is foregrounded. A new-concert alert is
    /// worth seeing without leaving the app.
    ///
    /// nonisolated for the same reason as above: UNNotification is not
    /// Sendable, and nothing here needs the main actor.
    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification
    ) async -> UNNotificationPresentationOptions {
        [.banner, .sound]
    }
}
