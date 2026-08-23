import Foundation
import Testing

@testable import ConcertFinder

struct FiltersTests {

    @Test func emptyFiltersProduceNoQueryItems() {
        #expect(Filters.empty.queryItems.isEmpty)
        #expect(Filters.empty.activeCount == 0)
        #expect(Filters.empty.isEmpty)
    }

    /// A date range counts as one active filter, not two — the badge should
    /// read what the user set, and they set one thing.
    @Test func activeCountTreatsDateRangeAsOne() {
        var filters = Filters.empty
        filters.dateFrom = "2026-09-01"
        filters.dateTo = "2026-09-30"
        #expect(filters.activeCount == 1)

        filters.genre = "indie rock"
        #expect(filters.activeCount == 2)
    }

    @Test func weekdayAllIsNotSentAsAFilter() {
        var filters = Filters.empty
        filters.weekday = .all
        #expect(filters.queryItems.isEmpty)

        filters.weekday = .weekend
        #expect(filters.queryItems.contains { $0.name == "weekday" && $0.value == "weekend" })
    }

    /// Genre matching on the server is exact-tag case-insensitive, not a
    /// substring — a "rock · 12" pill that also matched "indie rock" and
    /// "post-rock" returned forty. The client's job is simply not to alter
    /// the value.
    @Test func genreIsSentExactlyAsGiven() {
        var filters = Filters.empty
        filters.genre = "Hardcore Punk"
        let value = filters.queryItems.first { $0.name == "genre" }?.value
        #expect(value == "Hardcore Punk")
    }

    @Test func roundTripsThroughCodable() throws {
        var filters = Filters.empty
        filters.genre = "indie rock"
        filters.venue = "9:30 Club"
        filters.weekday = .weekday
        filters.dateFrom = "2026-09-01"

        let data = try JSONEncoder().encode(filters)
        let decoded = try JSONDecoder().decode(Filters.self, from: data)
        #expect(decoded == filters)
    }
}

struct APIErrorTests {

    /// Only failures the app can plausibly ride out over cached content are
    /// transient. A 401 or a decode failure must never leave stale data on
    /// screen pretending to be current.
    @Test func onlyNetworkAndServerFailuresAreTransient() {
        #expect(APIError.offline.isTransient)
        #expect(APIError.timedOut.isTransient)
        #expect(APIError.server(status: 503, message: nil).isTransient)

        #expect(!APIError.unauthorized.isTransient)
        #expect(!APIError.server(status: 400, message: nil).isTransient)
        #expect(!APIError.decoding("Event").isTransient)
        #expect(!APIError.updateRequired.isTransient)
    }

    /// The Development Mode 403 keeps the server's own wording: it names the
    /// remedy, and "try again" would be wrong advice.
    @Test func allowlistErrorPreservesServerMessage() {
        let detail = "This app is still in Spotify's development mode."
        #expect(APIError.notOnSpotifyAllowlist(detail).userMessage == detail)
    }

    @Test func throttledFallsBackToAGenericMessageWithoutAReason() {
        let withReason = APIError.throttled(retryAfter: nil, reason: "daily upstream quota exhausted")
        #expect(withReason.userMessage == "daily upstream quota exhausted")

        let withoutReason = APIError.throttled(retryAfter: nil, reason: nil)
        #expect(!withoutReason.userMessage.isEmpty)
    }
}
