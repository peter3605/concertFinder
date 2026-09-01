import Foundation
import Testing

@testable import ConcertFinder

/// A canned HTTP layer, injected through `APIClient`'s existing `session`
/// parameter so no production call site changes.
///
/// `nonisolated(unsafe)` on the route table: `URLProtocol` is registered as a
/// class, so there is no instance to hang configuration off. The suite below
/// is `.serialized` for exactly this reason — the table is process-wide and
/// two tests rewriting it concurrently would be a flake with no obvious cause.
final class StubURLProtocol: URLProtocol {
    struct Route: Sendable {
        let status: Int
        let body: Data

        init(status: Int, json: String = "") {
            self.status = status
            self.body = Data(json.utf8)
        }
    }

    nonisolated(unsafe) static var routes: [String: Route] = [:]

    /// A session wired to this stub and nothing else. Ephemeral so no response
    /// is cached between tests.
    static func session() -> URLSession {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [StubURLProtocol.self]
        return URLSession(configuration: configuration)
    }

    /// Not named `client`: `URLProtocol` already has an instance property by
    /// that name, and `startLoading` below depends on resolving to it.
    static func makeClient(tokens: any TokenStore) -> APIClient {
        APIClient(
            baseURL: URL(string: "https://stub.invalid")!,
            tokens: tokens,
            session: session()
        )
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let url = request.url ?? URL(string: "https://stub.invalid")!
        // An unrouted path answers 500 rather than hanging: a test that
        // forgot a route should fail, not time out.
        let route = Self.routes[url.path] ?? Route(status: 500)
        let response = HTTPURLResponse(
            url: url,
            statusCode: route.status,
            httpVersion: "HTTP/1.1",
            headerFields: ["Content-Type": "application/json"]
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: route.body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

actor StubTokenStore: TokenStore {
    private var token: String?
    private(set) var didClear = false

    init(token: String? = "session-token") {
        self.token = token
    }

    func currentToken() async -> String? { token }

    func clear() async {
        token = nil
        didClear = true
    }
}

/// Records that the handler fired, separately from the handler itself, so a
/// test can drop every reference to the handler and still observe it.
final class ExpiryFlag: @unchecked Sendable {
    private let lock = NSLock()
    private var value = false

    var didFire: Bool {
        lock.lock()
        defer { lock.unlock() }
        return value
    }

    func fire() {
        lock.lock()
        value = true
        lock.unlock()
    }
}

/// A counter a `@MainActor` callback can bump. A captured local `var` would
/// work too, but a box keeps the closure's isolation the only thing under test.
@MainActor
final class CallCounter {
    var value = 0
}

final class RecordingInvalidationHandler: SessionInvalidationHandler, @unchecked Sendable {
    private let flag: ExpiryFlag

    init(flag: ExpiryFlag) { self.flag = flag }

    func sessionDidExpire() async { flag.fire() }
}

@Suite(.serialized)
struct SessionLifecycleTests {

    // MARK: - Fixtures

    private static let concertsJSON = """
    {
      "location": {"latitude": 38.9, "longitude": -77.0, "radius_miles": 50, "is_default": true},
      "count": 1,
      "events": [{
        "event_key": "evt-1",
        "date": "2099-06-01T20:00:00Z",
        "venue": "9:30 Club",
        "city": "Washington",
        "state": "DC",
        "acts": [{"artist": {"id": "a1", "name": "Turnstile"}, "dedup_key": "d1"}],
        "links": []
      }],
      "facets": {"genres": [], "venues": []},
      "computed_at": "2099-05-30T12:00:00Z",
      "refreshing": false,
      "complete": true
    }
    """

    private static let savedJSON = """
    {
      "count": 1,
      "events": [{
        "event_key": "evt-1",
        "date": "2099-06-01T20:00:00Z",
        "venue": "9:30 Club",
        "city": "Washington",
        "state": "DC",
        "acts": [{"artist": {"id": "a1", "name": "Turnstile"}, "dedup_key": "d1", "saved": true}],
        "links": []
      }]
    }
    """

    private static let artistsJSON = """
    {"artists": [{"id": "a1", "name": "Turnstile", "genres": ["hardcore punk"]}]}
    """

    private static let locationJSON = """
    {"latitude": 38.9, "longitude": -77.0, "radius_miles": 50,
     "display_name": "Washington, DC", "is_default": false}
    """

    /// What `GET /me/location` answers for someone who has never chosen one:
    /// the deployment's fallback city, flagged as such.
    private static let defaultLocationJSON = """
    {"latitude": 38.9, "longitude": -77.0, "radius_miles": 50, "is_default": true}
    """

    private static func signedInRoutes() {
        StubURLProtocol.routes = [
            "/api/me/concerts": .init(status: 200, json: concertsJSON),
            "/api/me/saved-concerts": .init(status: 200, json: savedJSON),
            "/api/me/subscribed-artists": .init(status: 200, json: artistsJSON),
            "/api/me/location": .init(status: 200, json: locationJSON),
        ]
    }

    // MARK: - P1-1

    /// The bug: `invalidationHandler` was `weak` and the only object holding
    /// the bridge was the `setInvalidationHandler` call itself, so it was
    /// deallocated before the first request. A 401 then cleared the Keychain
    /// and left `auth.state` on `.signedIn` forever.
    ///
    /// The handler here is constructed inline on purpose — nothing but the
    /// client retains it, which is exactly the arrangement that used to fail.
    @Test func aRejectedSessionReachesTheInvalidationHandler() async {
        StubURLProtocol.routes = ["/api/auth/me": .init(status: 401)]
        let flag = ExpiryFlag()
        let tokens = StubTokenStore()
        let api = StubURLProtocol.makeClient(tokens: tokens)
        await api.setInvalidationHandler(RecordingInvalidationHandler(flag: flag))

        do {
            _ = try await api.me()
            Issue.record("a 401 should have thrown")
        } catch {
            #expect(error as? APIError == .unauthorized)
        }

        #expect(flag.didFire)
        let cleared = await tokens.didClear
        #expect(cleared)
    }

    // MARK: - P1-2

    @Test @MainActor func signOutEmptiesEveryModel() async {
        Self.signedInRoutes()
        let api = StubURLProtocol.makeClient(tokens: StubTokenStore())

        let feed = FeedModel(api: api)
        let saved = SavedModel(api: api)
        let artists = ArtistsModel(api: api)
        let location = LocationModel(api: api)

        await feed.load()
        await feed.refreshLocationState()
        await saved.load()
        await artists.load()
        await location.load()

        #expect(!feed.events.isEmpty)
        #expect(feed.computedAt != nil)
        #expect(!saved.events.isEmpty)
        #expect(!artists.subscribed.isEmpty)
        #expect(location.location != nil)
        #expect(!location.cityQuery.isEmpty)

        feed.reset()
        saved.reset()
        artists.reset()
        location.reset()

        #expect(feed.events.isEmpty)
        #expect(feed.computedAt == nil)
        #expect(feed.error == nil)
        #expect(!feed.isUsingFallbackLocation)
        #expect(saved.events.isEmpty)
        #expect(artists.subscribed.isEmpty)
        #expect(artists.results.isEmpty)
        #expect(location.location == nil)
        #expect(location.cityQuery.isEmpty)
    }

    /// Filters are one account's view of another account's facets, so the
    /// persisted blob has to go with everything else — otherwise the next
    /// person to sign in on this device opens an empty feed they did not
    /// filter.
    @Test @MainActor func resettingTheFeedClearsThePersistedFilters() async {
        Self.signedInRoutes()
        let feed = FeedModel(api: StubURLProtocol.makeClient(tokens: StubTokenStore()))

        var filters = Filters.empty
        filters.genre = "hardcore punk"
        FilterStore.save(filters)
        #expect(FilterStore.load() == filters)

        feed.reset()

        #expect(feed.filters == .empty)
        #expect(FilterStore.load() == .empty)
    }

    // MARK: - P1-5

    @Test @MainActor func aDeepLinkPushesTheEventItNames() async {
        Self.signedInRoutes()
        let feed = FeedModel(api: StubURLProtocol.makeClient(tokens: StubTokenStore()))
        await feed.load()

        await feed.openEvent(withKey: "evt-1")

        #expect(feed.path.count == 1)
        #expect(!feed.missingDeepLinkEvent)
    }

    /// A key the feed cannot produce even after a fetch is the case that used
    /// to land the user on an unchanged feed with no explanation at all.
    @Test @MainActor func anUnresolvableDeepLinkSaysSoInsteadOfPushingNothing() async {
        Self.signedInRoutes()
        let feed = FeedModel(api: StubURLProtocol.makeClient(tokens: StubTokenStore()))
        await feed.load()

        await feed.openEvent(withKey: "evt-does-not-exist")

        #expect(feed.path.isEmpty)
        #expect(feed.missingDeepLinkEvent)
    }

    // MARK: - P1-6

    @Test @MainActor func savingACityNotifiesTheFeed() async {
        Self.signedInRoutes()
        let location = LocationModel(api: StubURLProtocol.makeClient(tokens: StubTokenStore()))
        let calls = CallCounter()
        location.onLocationChanged = { calls.value += 1 }

        location.cityQuery = "Washington, DC"
        await location.saveCity()

        #expect(calls.value == 1)
    }

    // MARK: - P1-7

    /// A 429 is the server's answer, not a failure: it says when the throttle
    /// lifts and which limit was hit. It used to be stored in `error` and
    /// never rendered anywhere.
    @Test @MainActor func aRefusedRescanKeepsItsReasonOutOfTheErrorSlot() async {
        Self.signedInRoutes()
        StubURLProtocol.routes["/api/me/concerts/refresh"] = .init(
            status: 429,
            json: #"{"retry_after": "2099-01-01T00:00:00Z", "reason": "daily upstream quota exhausted"}"#
        )
        let feed = FeedModel(api: StubURLProtocol.makeClient(tokens: StubTokenStore()))

        await feed.requestRescan()

        #expect(feed.rescanRefusal?.reason == "daily upstream quota exhausted")
        #expect(feed.rescanRefusal?.until != nil)
        #expect(feed.error == nil)
        // `retryAfter` outranks everything: a scan asked for before it lifts
        // comes back capped by construction.
        #expect(!feed.canRescan)
    }

    @Test @MainActor func anAcceptedRescanRereadsTheSnapshot() async {
        Self.signedInRoutes()
        StubURLProtocol.routes["/api/me/concerts/refresh"] = .init(status: 200, json: "{}")
        let feed = FeedModel(api: StubURLProtocol.makeClient(tokens: StubTokenStore()))

        await feed.requestRescan()

        #expect(feed.rescanRefusal == nil)
        #expect(!feed.events.isEmpty)
    }

    // MARK: - P3-1

    /// Nothing may sit waiting on a scan of a city the user never named.
    ///
    /// Login no longer pre-warms a snapshot for a user with no location of
    /// their own, so the *feed read* is what enqueues the scan — and a scan
    /// is up to five minutes and a chunk of a 250-call daily allowance, all
    /// of it thrown away the moment they set their real city, which produces
    /// a fresh `location_key` with no snapshot and a second full scan.
    @Test @MainActor func aFirstRunAsksWhereYouAreBeforeItReadsTheFeed() async {
        await SnapshotCache.shared.clear()
        FirstRunTracker.reset()
        StubURLProtocol.routes = [
            "/api/me/concerts": .init(status: 200, json: Self.concertsJSON),
            "/api/me/location": .init(status: 200, json: Self.defaultLocationJSON),
        ]
        let feed = FeedModel(api: StubURLProtocol.makeClient(tokens: StubTokenStore()))

        await feed.start()

        #expect(feed.isAwaitingLocation)
        #expect(feed.isFirstRun, "the first-run screen has to render the question it is asking")
        #expect(feed.events.isEmpty, "the feed must not be read, because reading it starts the scan")
        // The question is asked, but the fallback coordinates are still known
        // — the signed-out backdrop has somewhere to search in the meantime.
        #expect(feed.searchOrigin != nil)

        await feed.continueWithoutLocation()

        #expect(!feed.isAwaitingLocation)
        #expect(!feed.events.isEmpty, "declining the question must not be a dead end")

        FirstRunTracker.reset()
        await SnapshotCache.shared.clear()
    }

    /// The gate is first-run only. Someone who has been using the app for
    /// months and never set a location still has a feed, and withholding it
    /// until they answer a question would be a worse bug than the one above.
    @Test @MainActor func anEstablishedUserWithNoLocationStillGetsTheirFeed() async {
        await SnapshotCache.shared.clear()
        FirstRunTracker.reset()
        FirstRunTracker.markCompleted()
        StubURLProtocol.routes = [
            "/api/me/concerts": .init(status: 200, json: Self.concertsJSON),
            "/api/me/location": .init(status: 200, json: Self.defaultLocationJSON),
        ]
        let feed = FeedModel(api: StubURLProtocol.makeClient(tokens: StubTokenStore()))

        await feed.start()

        #expect(!feed.isAwaitingLocation)
        #expect(!feed.isFirstRun)
        #expect(!feed.events.isEmpty)
        #expect(feed.isUsingFallbackLocation, "the banner asking them to set one still applies")

        FirstRunTracker.reset()
        await SnapshotCache.shared.clear()
    }

    // MARK: - P3-4

    /// Raised on the first settled scan with results in it, and never again —
    /// the system dialog behind it is a one-shot, so "once ever" is the whole
    /// contract.
    @Test @MainActor func theSoftPushPromptIsRaisedOnceEver() async {
        await SnapshotCache.shared.clear()
        FirstRunTracker.reset()
        Self.signedInRoutes()
        let feed = FeedModel(api: StubURLProtocol.makeClient(tokens: StubTokenStore()))

        await feed.load()
        #expect(feed.isShowingPushPrompt)
        #expect(FirstRunTracker.hasOfferedPushPrompt)

        // Dismissed without accepting. A second load must not ask again:
        // silence is an answer.
        feed.isShowingPushPrompt = false
        await feed.load()
        #expect(!feed.isShowingPushPrompt)

        FirstRunTracker.reset()
        await SnapshotCache.shared.clear()
    }

    // MARK: - P3-5

    /// A hint the user dismissed was postponed rather than dismissed if it
    /// comes back on the next launch, and the user has no way to tell the
    /// difference except by dismissing it again.
    @Test func theSaveVersusSubscribeHintStaysDismissed() {
        HintStore.reset()
        #expect(!HintStore.isDismissed(.saveVersusSubscribe))

        HintStore.dismiss(.saveVersusSubscribe)
        #expect(HintStore.isDismissed(.saveVersusSubscribe))

        // Sign-out clears it, so the next account on this device is
        // introduced to the pair too.
        HintStore.reset()
        #expect(!HintStore.isDismissed(.saveVersusSubscribe))
    }
}
