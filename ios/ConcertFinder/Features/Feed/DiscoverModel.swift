import Observation

/// The signed-out "popular shows near you" backdrop.
///
/// Deliberately the thinnest model in the app, and every omission is on
/// purpose:
///
/// - **No error state.** This sits behind the first screen a new user sees,
///   under a scan they are already waiting on. A failure here is worth
///   nothing to them and an error banner over it would be the app's first
///   impression. Nothing found and nothing loaded both render as nothing.
/// - **No polling and no cache.** The rows behind it are other users' scans,
///   rewritten hours apart; one read per appearance is the whole budget.
/// - **No saves, no subscribes, no `reason`.** The response carries none of
///   those, because it does not know who is asking. Anything here that
///   implied personalisation would be a claim the data cannot support.
@MainActor
@Observable
final class DiscoverModel {
    private(set) var events: [Event] = []

    private let api: APIClient
    /// The coordinates the loaded events belong to, so a second appearance at
    /// the same place does not repeat the request and a *changed* location —
    /// the user answering the first-run question — does.
    private var loadedKey: String?

    /// Enough to fill the space behind the first-run screen without turning
    /// it into a listings page. The server caps at 50.
    private static let maxEvents = 6

    init(api: APIClient) {
        self.api = api
    }

    func load(latitude: Double, longitude: Double, radiusMiles: Int) async {
        let key = "\(latitude),\(longitude),\(radiusMiles)"
        guard key != loadedKey else { return }
        guard let response = try? await api.discover(
            latitude: latitude,
            longitude: longitude,
            radiusMiles: radiusMiles
        ) else {
            // Silent by design — see the type's note. The caller renders
            // nothing either way, so there is nothing to distinguish.
            return
        }
        loadedKey = key
        events = Array(response.events.prefix(Self.maxEvents))
    }

    /// Sign-out. Nothing here belongs to an account, but leaving a previous
    /// city's shows behind the next person's first run would look like a feed
    /// built for them.
    func reset() {
        events = []
        loadedKey = nil
    }
}
