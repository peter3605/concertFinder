import Foundation

/// Disk cache for the last successful feed response.
///
/// This is nearly free and it converts a cold launch from a spinner into
/// content: the response *is* a snapshot the server already computed, so
/// rendering it immediately and showing its `computedAt` as "updated 3h ago"
/// is honest rather than stale (plan §5.5).
///
/// Stored in Caches, not Documents: it is reconstructible from the server, so
/// it should not be backed up to iCloud or count against the user's storage
/// in a way they cannot clear.
actor SnapshotCache {
    static let shared = SnapshotCache()

    private let filename = "feed-snapshot.json"

    private var directory: URL? {
        FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask).first
    }

    private var fileURL: URL? {
        directory?.appendingPathComponent(filename)
    }

    /// What was cached, plus when the app stored it.
    struct Entry: Codable, Sendable {
        let events: [Event]
        let facets: Facets
        let location: UserLocation
        let computedAt: Date?
        let complete: Bool
        /// When this app wrote the entry, as distinct from when the server
        /// computed the snapshot. The UI shows the server's time; this one
        /// exists to age the cache out.
        let cachedAt: Date
    }

    /// Entries older than this are ignored. A week-old feed is mostly past
    /// shows, which the server floors out anyway — showing it would be worse
    /// than showing the empty state.
    private static let maxAge: TimeInterval = 7 * 24 * 60 * 60

    private static let encoder: JSONEncoder = {
        let e = JSONEncoder()
        e.dateEncodingStrategy = .iso8601
        return e
    }()

    private static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .iso8601
        return d
    }()

    func store(_ response: ConcertsResponse) {
        guard let fileURL else { return }
        let entry = Entry(
            events: response.events,
            facets: response.facets,
            location: response.location,
            computedAt: response.computedAt,
            complete: response.complete,
            cachedAt: Date()
        )
        guard let data = try? Self.encoder.encode(entry) else { return }
        // Atomic so a crash mid-write leaves the previous entry rather than a
        // truncated file the decoder would reject on next launch.
        try? data.write(to: fileURL, options: .atomic)
    }

    func load() -> Entry? {
        guard let fileURL,
              let data = try? Data(contentsOf: fileURL),
              let entry = try? Self.decoder.decode(Entry.self, from: data)
        else {
            return nil
        }
        guard Date().timeIntervalSince(entry.cachedAt) < Self.maxAge else {
            clear()
            return nil
        }
        return entry
    }

    func clear() {
        guard let fileURL else { return }
        try? FileManager.default.removeItem(at: fileURL)
    }
}

/// The signed-in user, cached so a launch with no network can still render a
/// signed-in shell instead of bouncing to the login screen.
enum CachedProfile {
    private static var fileURL: URL? {
        FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)
            .first?.appendingPathComponent("profile.json")
    }

    static func save(_ me: Me) async throws {
        guard let fileURL else { return }
        try JSONEncoder().encode(me).write(to: fileURL, options: .atomic)
    }

    static func load() async throws -> Me? {
        guard let fileURL, let data = try? Data(contentsOf: fileURL) else { return nil }
        return try? JSONDecoder().decode(Me.self, from: data)
    }

    static func clear() async throws {
        guard let fileURL else { return }
        try? FileManager.default.removeItem(at: fileURL)
    }
}

/// Filter state, persisted locally. The server holds none of it, so this is
/// the only thing that makes the feed come back the way the user left it.
enum FilterStore {
    private static let key = "cf.filters"

    static func load() -> Filters {
        guard let data = UserDefaults.standard.data(forKey: key),
              let filters = try? JSONDecoder().decode(Filters.self, from: data)
        else {
            return .empty
        }
        return filters
    }

    static func save(_ filters: Filters) {
        guard let data = try? JSONEncoder().encode(filters) else { return }
        UserDefaults.standard.set(data, forKey: key)
    }
}
