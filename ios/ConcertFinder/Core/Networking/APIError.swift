import Foundation

/// Every failure the app can surface, named. URLError must not reach a view:
/// "The operation couldn't be completed" is not a thing a user can act on,
/// and the cases below each have a different correct response.
enum APIError: Error, Equatable {
    /// The session is gone — expired, deleted, or the Spotify grant was
    /// revoked. Handled in exactly one place (APIClient), which clears the
    /// Keychain and returns to login.
    case unauthorized

    /// Spotify Development Mode: the account is not on the operator's
    /// allowlist. A configuration state, not a user error, and it gets its
    /// own copy because "try again" is useless advice for it.
    case notOnSpotifyAllowlist(String)

    /// Manual refresh was throttled. `retryAfter` is when it lifts;
    /// `reason` distinguishes "you just refreshed" from "today's upstream
    /// allowance is gone", which are different messages.
    case throttled(retryAfter: Date?, reason: String?)

    /// The client build is older than the server's floor.
    case updateRequired

    /// No network. Distinguished from a server failure because the app can
    /// still show its cached snapshot.
    case offline

    case timedOut

    /// Non-2xx with a message the server provided.
    case server(status: Int, message: String?)

    /// The response did not match the model. Almost always a contract drift
    /// between the Go handlers and these Swift types — the case §8's contract
    /// test exists to catch before it ships.
    case decoding(String)

    case unknown(String)

    /// Whether showing a cached snapshot underneath the error makes sense.
    var isTransient: Bool {
        switch self {
        case .offline, .timedOut: true
        case .server(let status, _): status >= 500
        default: false
        }
    }

    var userMessage: String {
        switch self {
        case .unauthorized:
            "Your session expired. Please sign in again."
        case .notOnSpotifyAllowlist(let detail):
            detail
        case .throttled(_, let reason):
            reason ?? "You just refreshed. Give it a few minutes."
        case .updateRequired:
            "This version of ConcertFinder is no longer supported. Please update from the App Store."
        case .offline:
            "You're offline. Showing the last concerts we loaded."
        case .timedOut:
            "That took too long. Check your connection and try again."
        case .server(let status, let message):
            message ?? "Something went wrong on our end (\(status)). Try again shortly."
        case .decoding:
            "We couldn't read the response. Updating the app may help."
        case .unknown(let detail):
            detail
        }
    }
}
