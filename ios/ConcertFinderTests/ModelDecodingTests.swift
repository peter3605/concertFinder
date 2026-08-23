import Foundation
import Testing

@testable import ConcertFinder

/// Decoding tests against golden fixtures (plan §8).
///
/// **The fixtures in `Fixtures/` are generated, not hand-written.** They are
/// produced by `TestGoldenFixtures` in `internal/http`, which marshals the
/// real Go response structs. Do not edit them by hand — regenerate with:
///
///     go test ./internal/http -run TestGoldenFixtures -update
///
/// That is the whole contract check, and both halves are load-bearing:
/// renaming a `json` tag in Go fails the Go test, and regenerating without
/// updating these models fails the tests below. Two clients and no contract
/// test is how a field rename reaches the App Store.
///
/// This is not academic. The first run of the generator caught a fabricated
/// `is_default` on the feed's location object that the server never sends —
/// and a test here that asserted it was true, passing against the fabrication.
struct ModelDecodingTests {

    // The decoder configuration must match APIClient's exactly, or these
    // tests pass against rules the app does not use.
    static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        let withFraction = ISO8601DateFormatter()
        withFraction.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let plain = ISO8601DateFormatter()
        plain.formatOptions = [.withInternetDateTime]
        d.dateDecodingStrategy = .custom { decoder in
            let raw = try decoder.singleValueContainer().decode(String.self)
            if let date = withFraction.date(from: raw) ?? plain.date(from: raw) { return date }
            throw DecodingError.dataCorrupted(
                .init(codingPath: decoder.codingPath, debugDescription: "unparseable date: \(raw)")
            )
        }
        return d
    }()

    static func fixture(_ name: String) throws -> Data {
        let url = try #require(
            Bundle(for: BundleMarker.self).url(forResource: name, withExtension: "json"),
            "fixture \(name).json is not in the test bundle"
        )
        return try Data(contentsOf: url)
    }

    static func decode<T: Decodable>(_ type: T.Type, _ fixtureName: String) throws -> T {
        try decoder.decode(T.self, from: try fixture(fixtureName))
    }

    /// A festival with six acts is ONE card. This is the shape most likely to
    /// be got wrong, because the natural reading of "six matched artists" is
    /// six rows.
    @Test func decodesFestivalAsOneEventWithSixActs() throws {
        let response = try Self.decode(ConcertsResponse.self, "festival")

        #expect(response.count == 1)
        #expect(response.events.count == 1)

        let event = try #require(response.events.first)
        #expect(event.acts.count == 6)
        #expect(event.venue == "Merriweather Post Pavilion")
        #expect(event.location == "Columbia, MD")

        // Each act carries its own dedup key — they must all differ, or
        // save/unsave would act on the wrong artist.
        let keys = Set(event.acts.map(\.dedupKey))
        #expect(keys.count == 6)
    }

    /// Save and subscribe are per act even though the acts share a card.
    @Test func decodesPerActSavedAndSubscribedIndependently() throws {
        let response = try Self.decode(ConcertsResponse.self, "festival")
        let acts = try #require(response.events.first?.acts)

        let turnstile = try #require(acts.first { $0.artist.name == "Turnstile" })
        let snailMail = try #require(acts.first { $0.artist.name == "Snail Mail" })
        let beachHouse = try #require(acts.first { $0.artist.name == "Beach House" })

        #expect(turnstile.isSaved)
        #expect(!turnstile.isSubscribed)
        #expect(!snailMail.isSaved)
        #expect(snailMail.isSubscribed)
        // Absent flags decode as false, not nil-crash.
        #expect(!beachHouse.isSaved)
        #expect(!beachHouse.isSubscribed)
    }

    /// `computed_at` carries fractional seconds in this capture and does not
    /// in others. Both must parse — a single ISO8601 formatter handles only
    /// the variant it was configured for.
    @Test func decodesTimestampsWithAndWithoutFractionalSeconds() throws {
        let withFraction = try Self.decode(ConcertsResponse.self, "festival")
        #expect(withFraction.computedAt != nil)

        let withoutFraction = try Self.decode(ConcertsResponse.self, "empty-feed")
        #expect(withoutFraction.computedAt != nil)
    }

    @Test func decodesEmptyFeed() throws {
        let response = try Self.decode(ConcertsResponse.self, "empty-feed")

        #expect(response.events.isEmpty)
        #expect(response.count == 0)
        #expect(response.facets.genres.isEmpty)
        #expect(response.complete)
    }

    /// The feed's location object is a *narrower* struct than the one
    /// `GET /me/location` returns: no `display_name`, no `is_default`.
    ///
    /// Reading `isDefault` off the feed therefore yields nil — false — every
    /// time, so a "set your location" prompt driven from here would never
    /// appear and the user would be shown the deployment's fallback city as
    /// though they had chosen it. `FeedModel` asks `/me/location` instead.
    /// `TestFeedLocationHasNoDisplayFields` pins the Go side.
    @Test func feedLocationCarriesNoDisplayFields() throws {
        for fixture in ["festival", "empty-feed", "incomplete-scan"] {
            let response = try Self.decode(ConcertsResponse.self, fixture)
            #expect(response.location.isDefault == nil,
                    "\(fixture): the feed must not be a source of is_default")
            #expect(response.location.displayName == nil,
                    "\(fixture): the feed must not be a source of display_name")
            #expect(!response.location.usesFallback)
            // What it does carry, and what the scan actually used.
            #expect(response.location.radiusMiles > 0)
        }
    }

    /// Events carry venue coordinates, so the detail screen can drop an exact
    /// Maps pin rather than making Maps search a venue name. They are
    /// omitempty on the wire, so the client must treat them as optional.
    @Test func decodesEventCoordinates() throws {
        let response = try Self.decode(ConcertsResponse.self, "festival")
        let event = try #require(response.events.first)

        let latitude = try #require(event.latitude)
        let longitude = try #require(event.longitude)
        #expect(latitude != 0)
        #expect(longitude != 0)
    }

    /// `complete: false` and `retry_after` are the two states the UI has to
    /// tell apart from a quiet week.
    @Test func decodesIncompleteScanWithRetryAfter() throws {
        let response = try Self.decode(ConcertsResponse.self, "incomplete-scan")

        #expect(!response.complete)
        #expect(response.refreshing)
        #expect(response.retryAfter != nil)
    }

    @Test func decodesThrottledRefresh() throws {
        let throttled = try Self.decode(RefreshThrottled.self, "refresh-throttled")

        #expect(throttled.retryAfter != nil)
        // The reason is what distinguishes "you just refreshed" from
        // "today's allowance is gone", which are different messages.
        #expect(throttled.reason == "daily upstream quota exhausted")
    }

    /// Facet values go back to the server verbatim. A facet's count equals
    /// what clicking it returns, and the server matches venues under its own
    /// normalizer — so trimming or lowercasing here silently returns nothing.
    @Test func facetValuesSurviveVerbatimIntoQueryItems() throws {
        let response = try Self.decode(ConcertsResponse.self, "incomplete-scan")
        let venue = try #require(response.facets.venues.first)
        #expect(venue.value == "9:30 Club")

        var filters = Filters.empty
        filters.venue = venue.value
        let items = filters.queryItems
        let sent = try #require(items.first { $0.name == "venue" }?.value)
        #expect(sent == "9:30 Club", "the facet value must be sent unmodified")
    }

    @Test func ticketLinkLabelsCoverRetiredSources() {
        // Rows written before Bandsintown was removed still carry its links
        // until the janitor ages them out; the label is what keeps them
        // rendering as a name rather than a raw slug.
        #expect(TicketLink(source: "bandsintown", url: nil).label == "Bandsintown")
        #expect(TicketLink(source: "ticketmaster", url: nil).label == "Ticketmaster")
        #expect(TicketLink(source: "official", url: nil).label == "Official site")
    }
}

/// Anchors `Bundle(for:)` to the test bundle so fixtures resolve.
private final class BundleMarker {}
