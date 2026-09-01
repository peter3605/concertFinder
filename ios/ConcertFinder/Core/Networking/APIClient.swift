import Foundation

/// Where the app's credential lives, abstracted so APIClient can clear it on
/// a 401 without importing the auth layer.
protocol TokenStore: Sendable {
    func currentToken() async -> String?
    func clear() async
}

/// Notified when the session is rejected. One place, not thirteen (plan §5.3).
protocol SessionInvalidationHandler: AnyObject, Sendable {
    func sessionDidExpire() async
}

/// The single HTTP entry point.
///
/// An actor because the token, the shared URLSession, and the decoder are
/// mutable state touched from every screen — under Swift 6 strict
/// concurrency, that has to be isolated somewhere, and one actor is simpler
/// than making each caller reason about it.
actor APIClient {
    private let baseURL: URL
    private let session: URLSession
    private let tokens: TokenStore
    /// Strong. A weak reference here made the handler's lifetime a property of
    /// whoever happened to hold it, and the answer was "nobody" — it was gone
    /// before the first request and every 401 went unreported. The cycle this
    /// would otherwise close (client → handler → controller → client) is
    /// broken at the controller instead, where it is visible.
    private var invalidationHandler: (any SessionInvalidationHandler)?

    /// Identifies the client on every request, e.g. "ios/1.0.0 (build 42)".
    /// The server logs it; when something breaks for app users only, this is
    /// what separates them from browser traffic.
    private let clientHeader: String

    init(
        baseURL: URL,
        tokens: any TokenStore,
        session: URLSession = .shared,
        bundle: Bundle = .main
    ) {
        self.baseURL = baseURL
        self.tokens = tokens
        self.session = session
        let version = bundle.infoDictionary?["CFBundleShortVersionString"] as? String ?? "0.0.0"
        let build = bundle.infoDictionary?["CFBundleVersion"] as? String ?? "0"
        self.clientHeader = "ios/\(version) (build \(build))"
    }

    func setInvalidationHandler(_ handler: any SessionInvalidationHandler) {
        self.invalidationHandler = handler
    }

    /// This build's number, for the minimum-version check on launch.
    static func currentBuild(bundle: Bundle = .main) -> Int {
        Int(bundle.infoDictionary?["CFBundleVersion"] as? String ?? "0") ?? 0
    }

    // MARK: - Decoding

    /// The Go server emits RFC 3339. Some timestamps carry fractional
    /// seconds and some do not, and ISO8601DateFormatter accepts only the
    /// variant it was configured for — so both are tried rather than letting
    /// a `computedAt` with milliseconds fail the whole response.
    private static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        let withFraction = ISO8601DateFormatter()
        withFraction.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let plain = ISO8601DateFormatter()
        plain.formatOptions = [.withInternetDateTime]
        d.dateDecodingStrategy = .custom { decoder in
            let raw = try decoder.singleValueContainer().decode(String.self)
            if let date = withFraction.date(from: raw) ?? plain.date(from: raw) {
                return date
            }
            throw DecodingError.dataCorrupted(
                .init(codingPath: decoder.codingPath, debugDescription: "unparseable date: \(raw)")
            )
        }
        return d
    }()

    private static let encoder: JSONEncoder = {
        let e = JSONEncoder()
        e.keyEncodingStrategy = .convertToSnakeCase
        return e
    }()

    // MARK: - Request plumbing

    private func makeRequest(
        _ method: String,
        _ path: String,
        query: [URLQueryItem] = [],
        body: (any Encodable & Sendable)? = nil
    ) async throws -> URLRequest {
        guard var components = URLComponents(
            url: baseURL.appendingPathComponent(path),
            resolvingAgainstBaseURL: false
        ) else {
            throw APIError.unknown("Bad URL for \(path)")
        }
        if !query.isEmpty { components.queryItems = query }
        guard let url = components.url else {
            throw APIError.unknown("Bad URL for \(path)")
        }

        var request = URLRequest(url: url)
        request.httpMethod = method
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue(clientHeader, forHTTPHeaderField: "X-CF-Client")
        if let token = await tokens.currentToken() {
            // Bearer, never a cookie. This is also what makes the server skip
            // its CSRF check — a native client has no token to double-submit.
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        if let body {
            request.httpBody = try Self.encoder.encode(body)
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        return request
    }

    /// Runs a request and maps every failure onto APIError.
    @discardableResult
    private func perform(_ request: URLRequest) async throws -> Data {
        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch let error as URLError {
            switch error.code {
            case .notConnectedToInternet, .networkConnectionLost, .dataNotAllowed:
                throw APIError.offline
            case .timedOut:
                throw APIError.timedOut
            case .cancelled:
                throw CancellationError()
            default:
                throw APIError.unknown(error.localizedDescription)
            }
        }
        guard let http = response as? HTTPURLResponse else {
            throw APIError.unknown("Not an HTTP response")
        }

        switch http.statusCode {
        case 200...299:
            return data

        case 401:
            // Session expired, account deleted, or the Spotify grant was
            // revoked. Handled here so no caller has to.
            await tokens.clear()
            await invalidationHandler?.sessionDidExpire()
            throw APIError.unauthorized

        case 403:
            // Development Mode rejection arrives as a 403 with an
            // explanatory body. It is a configuration state the user cannot
            // fix by retrying, so it keeps the server's own wording.
            let message = String(data: data, encoding: .utf8)?
                .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            if message.localizedCaseInsensitiveContains("development mode") {
                throw APIError.notOnSpotifyAllowlist(message)
            }
            throw APIError.server(status: 403, message: message.isEmpty ? nil : message)

        case 429:
            let throttled = try? Self.decoder.decode(RefreshThrottled.self, from: data)
            throw APIError.throttled(retryAfter: throttled?.retryAfter, reason: throttled?.reason)

        default:
            let message = String(data: data, encoding: .utf8)?
                .trimmingCharacters(in: .whitespacesAndNewlines)
            throw APIError.server(status: http.statusCode, message: message?.isEmpty == false ? message : nil)
        }
    }

    private func decode<T: Decodable>(_ type: T.Type, from data: Data) throws -> T {
        do {
            return try Self.decoder.decode(T.self, from: data)
        } catch {
            throw APIError.decoding("\(T.self): \(error)")
        }
    }

    private func get<T: Decodable>(_ path: String, query: [URLQueryItem] = [], as type: T.Type) async throws -> T {
        let data = try await perform(try await makeRequest("GET", path, query: query))
        return try decode(T.self, from: data)
    }

    // MARK: - Auth

    func exchangeMobileCode(code: String, verifier: String) async throws -> MobileExchangeResponse {
        struct Body: Encodable, Sendable {
            let code: String
            let verifier: String
        }
        // Deliberately not through `get`: this is the one call that must not
        // attach a bearer token, since acquiring one is its purpose.
        let request = try await makeRequest(
            "POST", "api/auth/mobile/exchange",
            body: Body(code: code, verifier: verifier)
        )
        let data = try await perform(request)
        return try decode(MobileExchangeResponse.self, from: data)
    }

    func me() async throws -> Me {
        try await get("api/auth/me", as: Me.self)
    }

    func logout() async throws {
        _ = try await perform(try await makeRequest("POST", "api/auth/logout"))
    }

    func siteInfo() async throws -> SiteInfo {
        try await get("api/site-info", as: SiteInfo.self)
    }

    // MARK: - Feed

    func concerts(filters: Filters) async throws -> ConcertsResponse {
        try await get("api/me/concerts", query: filters.queryItems, as: ConcertsResponse.self)
    }

    /// Manual refresh. Throws `.throttled` with the instant it lifts when the
    /// server refuses — a manual tap must never bypass the quota guard.
    func refreshConcerts() async throws {
        _ = try await perform(try await makeRequest("POST", "api/me/concerts/refresh"))
    }

    // MARK: - Saved

    func savedConcerts() async throws -> SavedConcertsResponse {
        try await get("api/me/saved-concerts", as: SavedConcertsResponse.self)
    }

    func save(dedupKey: String) async throws {
        struct Body: Encodable, Sendable { let dedupKey: String }
        _ = try await perform(try await makeRequest("POST", "api/me/saved-concerts", body: Body(dedupKey: dedupKey)))
    }

    func unsave(dedupKey: String) async throws {
        let escaped = dedupKey.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? dedupKey
        _ = try await perform(try await makeRequest("DELETE", "api/me/saved-concerts/\(escaped)"))
    }

    // MARK: - Artists

    func subscribedArtists() async throws -> [SubscribedArtist] {
        struct Response: Decodable, Sendable { let artists: [SubscribedArtist]? }
        let response = try await get("api/me/subscribed-artists", as: Response.self)
        return response.artists ?? []
    }

    func searchArtists(query: String) async throws -> [SubscribedArtist] {
        struct Response: Decodable, Sendable { let artists: [SubscribedArtist]? }
        let response = try await get(
            "api/me/artists/search",
            query: [URLQueryItem(name: "q", value: query)],
            as: Response.self
        )
        return response.artists ?? []
    }

    func subscribe(artistID: String) async throws {
        _ = try await perform(try await makeRequest("POST", "api/me/subscribed-artists/\(artistID)"))
    }

    func unsubscribe(artistID: String) async throws {
        _ = try await perform(try await makeRequest("DELETE", "api/me/subscribed-artists/\(artistID)"))
    }

    // MARK: - Location

    func location() async throws -> UserLocation {
        try await get("api/me/location", as: UserLocation.self)
    }

    func setLocation(latitude: Double, longitude: Double, radiusMiles: Int) async throws -> UserLocation {
        struct Body: Encodable, Sendable {
            let latitude: Double
            let longitude: Double
            let radiusMiles: Int
        }
        let request = try await makeRequest(
            "PUT", "api/me/location",
            body: Body(latitude: latitude, longitude: longitude, radiusMiles: radiusMiles)
        )
        return try decode(UserLocation.self, from: try await perform(request))
    }

    func setLocation(query: String, radiusMiles: Int) async throws -> UserLocation {
        struct Body: Encodable, Sendable {
            let query: String
            let radiusMiles: Int
        }
        let request = try await makeRequest(
            "PUT", "api/me/location",
            body: Body(query: query, radiusMiles: radiusMiles)
        )
        return try decode(UserLocation.self, from: try await perform(request))
    }

    // MARK: - Preferences and account

    func updatePreferences(digest: Bool? = nil, instantNotify: Bool? = nil, push: Bool? = nil) async throws {
        struct Body: Encodable, Sendable {
            let digestOptIn: Bool?
            let instantNotifyOptIn: Bool?
            let pushOptIn: Bool?
        }
        _ = try await perform(try await makeRequest(
            "PUT", "api/me/email-prefs",
            body: Body(digestOptIn: digest, instantNotifyOptIn: instantNotify, pushOptIn: push)
        ))
    }

    /// Deletes the account. `confirmName` must match the signed-in user's
    /// display name, case- and whitespace-insensitively.
    ///
    /// The body is not optional and its absence was a real defect: this method
    /// used to send none at all, and the handler decodes one unconditionally,
    /// so every in-app deletion failed with 400 "invalid body: EOF". Nothing on
    /// the client distinguished that from any other error, and account
    /// deletion has to work in-app for App Store Guideline 5.1.1(v) -- so the
    /// one path a reviewer is guaranteed to exercise was the broken one.
    func deleteAccount(confirmName: String) async throws {
        struct Body: Encodable, Sendable {
            let confirmName: String
        }
        _ = try await perform(try await makeRequest(
            "DELETE", "api/me/account", body: Body(confirmName: confirmName)
        ))
    }

    /// Disconnects Spotify without deleting the account: the credential and
    /// the profile derived from it go, saves and subscriptions stay. Every
    /// session ends, so the caller must sign out locally afterwards.
    func disconnectSpotify() async throws {
        _ = try await perform(try await makeRequest("DELETE", "api/me/spotify-connection"))
    }

    // MARK: - Devices

    func registerDevice(token: String, environment: String) async throws {
        struct Body: Encodable, Sendable {
            let deviceToken: String
            let environment: String
        }
        _ = try await perform(try await makeRequest(
            "POST", "api/me/devices",
            body: Body(deviceToken: token, environment: environment)
        ))
    }

    func deregisterDevice(token: String) async throws {
        _ = try await perform(try await makeRequest("DELETE", "api/me/devices/\(token)"))
    }
}
