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
    /// Where this act sits on the bill, when the server had any basis for
    /// saying. Inferred from position in Ticketmaster's attraction list,
    /// which is not documented as billing order -- see
    /// internal/concerts/billing.go. `nil` means unknown, which is the honest
    /// answer for everything found through the Phase 2 fallback chain, and
    /// must render as nothing rather than as "Support".
    var billing: String?
    /// Why this artist is in the feed — one short line derived from the
    /// strongest signal in the user's affinity profile ("You follow them",
    /// "#7 in your top artists"). Absent in three ways that all happen, so it
    /// must render as nothing rather than as a placeholder: the server omits
    /// it when it has nothing honest to say about an artist, a profile
    /// computed by an older build carries none at all, and `/api/discover`
    /// has no user to say it about.
    var reason: String?

    var id: String { dedupKey }
    var isSaved: Bool { saved ?? false }
    var isSubscribed: Bool { subscribed ?? false }

    enum Billing: String {
        case headliner
        case support
    }

    var billingSlot: Billing? { billing.flatMap(Billing.init(rawValue:)) }
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
    /// Venue coordinates. Present on most events and omitted when the source
    /// gave none, so always optional — but when they are here, use them: they
    /// put an exact pin in Maps instead of making it search for a venue name,
    /// which is ambiguous for chains and multi-room complexes.
    let latitude: Double?
    let longitude: Double?
    var acts: [Act]
    let links: [TicketLink]
    /// The promoter's title for the night -- a festival or a package tour.
    /// The server omits it when it would only repeat an act's name, which is
    /// what Ticketmaster calls an ordinary club show, so a value here is
    /// always worth showing.
    var name: String?
    /// Ticketmaster's own classification rather than a guess from the name.
    /// Sparse: `nil`/false means "not marked", not "definitely not one".
    var isFestival: Bool?

    var id: String { eventKey }

    var location: String {
        guard let state, !state.isEmpty else { return city }
        return "\(city), \(state)"
    }
}

/// A saved search location.
///
/// **Two endpoints return different subsets of this.** `GET /me/location`
/// sends all five fields. `GET /me/concerts` embeds a narrower struct —
/// latitude, longitude, radius and nothing else — so `displayName` and
/// `isDefault` are always nil there.
///
/// That asymmetry is easy to miss and fails silently: reading `isDefault` off
/// the feed response yields false every time, so a "set your location" prompt
/// driven from it never fires and the user is shown the deployment's fallback
/// city as though they had chosen it. Ask `/me/location` for that answer.
/// `TestFeedLocationHasNoDisplayFields` in internal/http pins the distinction.
struct UserLocation: Codable, Hashable, Sendable {
    let latitude: Double
    let longitude: Double
    let radiusMiles: Int
    let displayName: String?
    /// True when the server served the deployment-wide fallback because the
    /// user has no saved location. Only meaningful from `GET /me/location`.
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
    /// The account has no Spotify identity and no artist or genre picks, so
    /// there is nothing to scan and the server queued nothing. Polling would
    /// wait forever: onboarding's picks step is the answer. Omitted when
    /// false, so optional.
    var needsPicks: Bool? = nil
    /// The genre part of this feed is a capped, date-sorted listing holding
    /// every genre show before this venue-local day ("2006-01-02") and none
    /// on or after it. Omitted when nothing was cut.
    var genreHorizon: String? = nil

    var requiresPicks: Bool { needsPicks ?? false }
}

struct SavedConcertsResponse: Codable, Sendable {
    let count: Int
    let events: [Event]
}

/// `GET /api/discover` — "popular shows near you", with no session.
///
/// The `Event` shape is the feed's, deliberately, so both clients reuse their
/// models and their card. What it does **not** carry is the point: no facets,
/// no `computed_at`, no `refreshing`, no `complete`, and acts with no artist
/// id, no `saved`, no `subscribed` and no `reason`. This response knows
/// nothing about who is asking, so nothing rendering it may imply that it
/// does — the label is "popular shows near you", never "your" anything.
struct DiscoverResponse: Codable, Sendable {
    let location: UserLocation
    let count: Int
    let events: [Event]
}

struct SubscribedArtist: Codable, Hashable, Identifiable, Sendable {
    let id: String
    let name: String
    let genres: [String]?

    /// What the Artists screen shows. Builds up to 1.0.0 (2) subscribed with
    /// no name, and the server answers those rows with `""`; until the user
    /// re-subscribes from a fixed build they stay blank, so the row must not
    /// trust the data (CF-42).
    var displayName: String {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? Self.unnamedFallback : trimmed
    }

    static let unnamedFallback = "Unnamed artist"
}

struct ArtistSearchResponse: Codable, Sendable {
    let artists: [SubscribedArtist]
}

/// The current user.
struct Me: Codable, Sendable, Equatable {
    let id: String
    /// Empty for an account that never signed in with Spotify (Apple or
    /// email). A disconnected Spotify account keeps it.
    let spotifyUserId: String
    /// Empty until onboarding asks, for an email account and for an Apple one
    /// whose name was not shared.
    var displayName: String
    var email: String?
    var digestOptIn: Bool?
    var instantNotifyOptIn: Bool?
    var pushOptIn: Bool?
    /// Providers this account can sign in with: "apple", "email", "spotify".
    /// Optional because a cached profile written by an older build has none.
    var authMethods: [String]?

    var hasEmail: Bool { !(email ?? "").isEmpty }

    /// Whether anything this account sees can be Spotify-derived. Decides
    /// the "Powered by Spotify" attribution and which artist search to use.
    /// It is the identity, never "has picks": a Spotify account with picks
    /// still sees Spotify-derived artists, and `/me/artists/search` is a 403
    /// for everyone else (CF-32). Mirrors `hasSpotify` in web types.ts.
    var hasSpotify: Bool { !spotifyUserId.isEmpty }

    /// A name to show for the account. The Spotify ID was the old fallback
    /// and is empty for everyone who signed in another way.
    var accountName: String {
        if !displayName.isEmpty { return displayName }
        if !spotifyUserId.isEmpty { return spotifyUserId }
        if let email, !email.isEmpty { return email }
        return "Your account"
    }
}

// MARK: - Picks (CF-30, CF-31)

/// `GET /api/me/picks` row. The name is Ticketmaster's, never the client's.
struct ArtistPick: Codable, Hashable, Identifiable, Sendable {
    let attractionId: String
    let name: String
    let genre: String?

    var id: String { attractionId }
}

struct ArtistPicksResponse: Codable, Sendable {
    let picks: [ArtistPick]?
    let max: Int
}

/// `GET /api/me/picks/search` result.
struct PickSearchResult: Codable, Hashable, Identifiable, Sendable {
    let attractionId: String
    let name: String
    let genre: String?
    let imageUrl: String?
    let upcoming: Int
    /// The name matches the query under folding: the likely answer.
    let exact: Bool

    var id: String { attractionId }

    /// The subscription ID a picks feed's acts carry, so the bell and
    /// instant alerts match. A Spotify account subscribes by Spotify ID.
    var subscriptionID: String { "tm:\(attractionId)" }
}

struct PickSearchResponse: Codable, Sendable {
    let query: String
    let results: [PickSearchResult]?
}

/// `GET /api/me/genres` row.
struct GenrePick: Codable, Hashable, Identifiable, Sendable {
    let genreId: String
    let name: String

    var id: String { genreId }
}

struct GenrePicksResponse: Codable, Sendable {
    let picks: [GenrePick]?
    let max: Int
}

/// A top-level Ticketmaster music genre from `GET /api/me/genres/available`.
struct TMGenre: Codable, Hashable, Identifiable, Sendable {
    let id: String
    let name: String
}

struct GenreCatalogResponse: Codable, Sendable {
    let genres: [TMGenre]?
}

/// Response from POST /api/auth/mobile/exchange, and the same body from
/// every other app sign-in: Apple native, the email link and the email code
/// (`writeBearerSession`), so one model covers all of them.
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
    /// Whether this deployment gates NEW accounts on an invite code. Optional
    /// so an older server that does not send it decodes cleanly, and absent
    /// means "do not show the field" -- the same thing every build shipped
    /// before the gate existed already does.
    ///
    /// It only decides whether the sign-in screen offers a text box. The gate
    /// itself lives in the auth callback, which is the only place that can
    /// tell a signup from a returning user, so a client that got this wrong
    /// would show or hide a field and could never admit anybody.
    let inviteRequired: Bool?
    /// Whether `/api/auth/apple/*` is mounted. A rendering hint: absent or
    /// false hides the button, since tapping it would only reach a 501.
    var appleSignIn: Bool? = nil
    /// Whether a new Spotify account could still be created. Spotify stays
    /// capped at five, so this is false almost always; it only decides copy.
    var spotifySignupsOpen: Bool? = nil
}

/// `GET /api/auth/invite`: whether a code would be accepted now and whether
/// it is a Spotify invite (CF-B33). Optional fields so a malformed or older
/// answer decodes as "no", which hides Spotify sign-up rather than offering a
/// path that ends in a refusal.
struct InviteCheck: Codable, Sendable, Equatable {
    var usable: Bool? = nil
    var spotify: Bool? = nil

    /// Spotify sign-up is offered only for a usable Spotify invite.
    var admitsSpotify: Bool { usable == true && spotify == true }
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
