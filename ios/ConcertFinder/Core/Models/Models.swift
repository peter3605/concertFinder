import Foundation

// Codable mirrors of web/src/lib/types.ts. These are transliterations, not a
// translation: the backend serves one JSON shape to both clients (plan §1.1),
// so any divergence here is a bug rather than an adaptation.
//
// Every property name matches the wire key exactly, which is why there are no
// CodingKeys enums — the decoder is configured with
// .convertFromSnakeCase in APIClient.

/// One of the user's artists, as the API returns it.
struct Artist: Codable, Hashable, Identifiable, Sendable {
    let id: String
    let name: String
    let genres: [String]?
}

struct TicketLink: Codable, Hashable, Sendable {
    let source: String
    let url: URL?

    /// Human label for a link source. Mirrors SOURCE_LABELS in types.ts,
    /// including entries for sources no longer produced: rows written before
    /// Bandsintown was removed still carry those links, and the label is what
    /// keeps them rendering as a name rather than a raw slug until the
    /// janitor ages them out.
    var label: String {
        switch source {
        case "ticketmaster": "Ticketmaster"
        case "official": "Official site"
        case "songkick": "Songkick"
        case "bandsintown": "Bandsintown"
        default: source.capitalized
        }
    }
}

/// One of the user's artists on a bill.
///
/// Save and subscribe are per act even though acts share a card — each
/// carries its own `dedupKey`. Getting this wrong produces a festival card
/// where tapping the bookmark saves the wrong artist.
struct Act: Codable, Hashable, Identifiable, Sendable {
    let artist: Artist
    let dedupKey: String
    var saved: Bool?
    var subscribed: Bool?

    var id: String { dedupKey }
    var isSaved: Bool { saved ?? false }
    var isSubscribed: Bool { subscribed ?? false }
}

/// One show — one card, one night out.
///
/// A festival the user matched six artists at is a single Event with six
/// acts, not six cards.
struct Event: Codable, Hashable, Identifiable, Sendable {
    let eventKey: String
    /// The *earliest* act's set time. Acts at a festival start at different
    /// times, so this is for sorting and month grouping only — never present
    /// it as when a particular act plays.
    let date: Date
    let venue: String
    let city: String
    let state: String?
    let country: String?
    var acts: [Act]
    let links: [TicketLink]

    var id: String { eventKey }

    var location: String {
        guard let state, !state.isEmpty else { return city }
        return "\(city), \(state)"
    }
}

struct UserLocation: Codable, Hashable, Sendable {
    let latitude: Double
    let longitude: Double
    let radiusMiles: Int
    let displayName: String?
    /// True when the server served the deployment-wide fallback because the
    /// user has no saved location. Drives a "set your location" prompt rather
    /// than silently showing someone else's city.
    let isDefault: Bool?

    var usesFallback: Bool { isDefault ?? false }
}

struct Facet: Codable, Hashable, Identifiable, Sendable {
    let value: String
    let count: Int

    var id: String { value }
}

struct Facets: Codable, Hashable, Sendable {
    let genres: [Facet]
    let venues: [Facet]

    static let empty = Facets(genres: [], venues: [])
}

/// The feed response. Three fields here are UI states the app must respond
/// to, not diagnostics.
struct ConcertsResponse: Codable, Sendable {
    let location: UserLocation
    /// Number of *events* — one per card. Not a count of artist matches,
    /// which would report a festival as six.
    let count: Int
    let events: [Event]
    let facets: Facets
    let computedAt: Date?
    /// A scan is in flight; poll for the result.
    let refreshing: Bool
    /// False when the scan behind these results did not cover every artist.
    /// A quiet Tuesday and a truncated scan look identical without this.
    let complete: Bool
    /// Set when the shortfall was the daily upstream quota, which resets at
    /// this time. Absent when another scan could help sooner.
    let retryAfter: Date?
}

struct SavedConcertsResponse: Codable, Sendable {
    let count: Int
    let events: [Event]
}

struct SubscribedArtist: Codable, Hashable, Identifiable, Sendable {
    let id: String
    let name: String
    let genres: [String]?
}

struct ArtistSearchResponse: Codable, Sendable {
    let artists: [SubscribedArtist]
}

/// The current user.
struct Me: Codable, Sendable, Equatable {
    let id: String
    let spotifyUserId: String
    let displayName: String
    let email: String?
    var digestOptIn: Bool?
    var instantNotifyOptIn: Bool?
    var pushOptIn: Bool?

    var hasEmail: Bool { !(email ?? "").isEmpty }
}

/// Response from POST /api/auth/mobile/exchange.
struct MobileExchangeResponse: Codable, Sendable {
    let sessionToken: String
    let expiresAt: Date
    let user: Me
}

struct SiteInfo: Codable, Sendable {
    let contactEmail: String
    let effectiveDate: String
    /// Oldest build this server supports. The app compares it on launch and
    /// shows a blocking update screen below it.
    let minIosBuild: Int?
}

/// A 429 from the refresh endpoint.
struct RefreshThrottled: Codable, Sendable {
    let retryAfter: Date?
    let reason: String?
}

// MARK: - Filters

enum Weekday: String, Codable, CaseIterable, Sendable {
    case all
    case weekday
    case weekend

    var label: String {
        switch self {
        case .all: "Any day"
        case .weekday: "Weekdays"
        case .weekend: "Weekends"
        }
    }
}

/// Filter state. Held entirely on the client — the server keeps none of it —
/// and persisted locally so the feed comes back the way the user left it.
struct Filters: Codable, Equatable, Sendable {
    var genre: String = ""
    /// Venue name exactly as the facet list gave it. The server compares it
    /// under its own normalization, so it must be sent back verbatim — do not
    /// lowercase or trim it.
    var venue: String = ""
    var dateFrom: String = ""
    var dateTo: String = ""
    var weekday: Weekday = .all

    static let empty = Filters()

    var isEmpty: Bool { self == .empty }

    /// How many filters are active, for the badge on the filter button.
    var activeCount: Int {
        var n = 0
        if !genre.isEmpty { n += 1 }
        if !venue.isEmpty { n += 1 }
        if !dateFrom.isEmpty || !dateTo.isEmpty { n += 1 }
        if weekday != .all { n += 1 }
        return n
    }

    var queryItems: [URLQueryItem] {
        var items: [URLQueryItem] = []
        if !genre.isEmpty { items.append(.init(name: "genre", value: genre)) }
        if !venue.isEmpty { items.append(.init(name: "venue", value: venue)) }
        if !dateFrom.isEmpty { items.append(.init(name: "date_from", value: dateFrom)) }
        if !dateTo.isEmpty { items.append(.init(name: "date_to", value: dateTo)) }
        if weekday != .all { items.append(.init(name: "weekday", value: weekday.rawValue)) }
        return items
    }
}
