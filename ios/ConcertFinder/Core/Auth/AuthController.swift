import AuthenticationServices
import CryptoKit
import Foundation
import Observation
import UIKit

/// Drives the OAuth handshake and owns the app's notion of "signed in".
///
/// The app never sees a Spotify token. All Spotify access stays
/// server-mediated, which docs/design.md §2 calls non-negotiable and which is
/// also the only reason the App Store analysis in plan §10.1 is arguable at
/// all. What the app holds is a ConcertFinder session, nothing more.
@MainActor
@Observable
final class AuthController: NSObject {
    enum State: Equatable {
        case loading
        case signedOut
        case signedIn(Me)
        /// The server's minimum build is above this one. Blocking — there is
        /// nothing useful the app can do against an API it cannot speak to.
        case updateRequired
    }

    private(set) var state: State = .loading
    private(set) var authError: APIError?

    private let api: APIClient
    private let tokens: KeychainTokenStore
    private let baseURL: URL
    private var webAuthSession: ASWebAuthenticationSession?

    /// Set after a successful sign-in so the push registrar can run. Kept as
    /// a closure rather than a delegate so AuthController does not depend on
    /// the push layer.
    var onSignIn: (@MainActor (Me) -> Void)?
    /// Async and awaited rather than fire-and-forget: it deregisters the APNs
    /// device, which is an authenticated call, and on the deliberate sign-out
    /// path the very next line throws the token away. A `Task` here raced that
    /// and the loser came back 401 — reported as a session expiry, which
    /// stamps "your session expired" over a sign-out the user asked for.
    var onSignOut: (@MainActor () async -> Void)?

    init(api: APIClient, tokens: KeychainTokenStore, baseURL: URL) {
        self.api = api
        self.tokens = tokens
        self.baseURL = baseURL
        super.init()
    }

    /// Resolves the stored session on launch.
    ///
    /// The minimum-build check runs first and unauthenticated, because an
    /// out-of-date client should be told to update rather than shown a
    /// half-working app — and /api/site-info is the one endpoint that is
    /// guaranteed to answer either way.
    func restore() async {
        if await isBuildTooOld() {
            state = .updateRequired
            return
        }
        guard await tokens.currentToken() != nil else {
            state = .signedOut
            return
        }
        do {
            state = .signedIn(try await api.me())
        } catch APIError.unauthorized {
            // The 401 path in APIClient already cleared the Keychain.
            state = .signedOut
        } catch {
            // A transient failure must not sign the user out — the token is
            // probably fine and the network is not. Trust the stored session
            // and let the feed surface the error against its cached snapshot.
            if let apiError = error as? APIError, apiError.isTransient,
               let cached = try? await CachedProfile.load() {
                state = .signedIn(cached)
            } else {
                state = .signedOut
            }
        }
    }

    private func isBuildTooOld() async -> Bool {
        guard let info = try? await api.siteInfo(), let floor = info.minIosBuild, floor > 0 else {
            // No floor published, or the check itself failed. Failing open is
            // right: a site-info blip must not brick every installed copy.
            return false
        }
        return APIClient.currentBuild() < floor
    }

    // MARK: - Sign in

    /// Runs the full login: PKCE against us, PKCE against Spotify inside the
    /// web context, then a one-time code exchanged for the session.
    func signIn() async {
        authError = nil

        // Our half of the handshake. The verifier never leaves the device,
        // which is what makes an intercepted callback URL useless.
        let verifier = Self.randomVerifier()
        let challenge = Self.challenge(for: verifier)

        var components = URLComponents(url: baseURL.appendingPathComponent("api/auth/login"),
                                       resolvingAgainstBaseURL: false)
        components?.queryItems = [
            URLQueryItem(name: "client", value: "ios"),
            URLQueryItem(name: "app_challenge", value: challenge),
        ]
        guard let loginURL = components?.url else {
            authError = .unknown("Could not build the sign-in URL.")
            return
        }

        do {
            let callbackURL = try await presentWebAuth(startingAt: loginURL)
            guard let code = URLComponents(url: callbackURL, resolvingAgainstBaseURL: false)?
                .queryItems?.first(where: { $0.name == "code" })?.value
            else {
                authError = .unknown("Sign-in finished without a code. Please try again.")
                return
            }
            let result = try await api.exchangeMobileCode(code: code, verifier: verifier)
            await tokens.store(result.sessionToken)
            try? await CachedProfile.save(result.user)
            state = .signedIn(result.user)
            onSignIn?(result.user)
        } catch let error as APIError {
            authError = error
        } catch is CancellationError {
            // The user dismissed the sheet. Not an error worth showing.
        } catch {
            authError = .unknown(error.localizedDescription)
        }
    }

    /// The redirect that ends the auth sheet: MOBILE_CALLBACK_URL's host and
    /// path. Derived from the API origin rather than written out, so the two
    /// cannot drift -- the server builds the same link from SITE_BASE_URL, and
    /// config.Validate refuses to start if those hosts disagree.
    ///
    /// This must be a real callback and not nil. `callbackURLScheme: nil` asks
    /// the session to match nothing, so the sheet lands on the callback page
    /// and sits there forever: the user sees Spotify accept their login and
    /// then nothing at all, with no error to report. That is how this shipped
    /// the first time it was run on a device.
    ///
    /// `.https` requires the `webcredentials` service for this host in both
    /// the entitlements and the apple-app-site-association file -- a different
    /// service from the `applinks` entry that routes the link itself. iOS
    /// refuses to start the session without it, and the simulator does not
    /// enforce it, so it fails only on a real phone.
    private static func authCallback(for baseURL: URL) -> ASWebAuthenticationSession.Callback {
        .https(host: baseURL.host() ?? "concertfinder.app", path: "/app/auth/callback")
    }

    private func presentWebAuth(startingAt url: URL) async throws -> URL {
        try await withCheckedThrowingContinuation { continuation in
            let session = ASWebAuthenticationSession(
                url: url,
                callback: Self.authCallback(for: baseURL)
            ) { callbackURL, error in
                if let error {
                    let code = (error as NSError).code
                    if code == ASWebAuthenticationSessionError.canceledLogin.rawValue {
                        continuation.resume(throwing: CancellationError())
                    } else {
                        continuation.resume(throwing: APIError.unknown(error.localizedDescription))
                    }
                    return
                }
                guard let callbackURL else {
                    continuation.resume(throwing: APIError.unknown("Sign-in returned nothing."))
                    return
                }
                continuation.resume(returning: callbackURL)
            }
            session.presentationContextProvider = self
            // false so a user already signed into Spotify in Safari does not
            // have to retype a password. Plan §5.4.
            session.prefersEphemeralWebBrowserSession = false
            self.webAuthSession = session
            session.start()
        }
    }

    // MARK: - Sign out

    func signOut() async {
        // First, while the token still exists: the handler clears the
        // in-memory models and deregisters the device, and the second of
        // those needs a session the lines below are about to destroy.
        await onSignOut?()
        // Best effort: the local session must be cleared even if the network
        // call fails, or a user who taps "sign out" offline stays signed in.
        try? await api.logout()
        await tokens.clear()
        try? await CachedProfile.clear()
        await SnapshotCache.shared.clear()
        // A different account on this device gets its own first run.
        FirstRunTracker.reset()
        state = .signedOut
    }

    /// Called by APIClient when any request 401s.
    ///
    /// The same teardown as `signOut`, minus the network calls the dead
    /// session can no longer make. Skipping it left the previous account's
    /// concerts, saves and artists on screen behind the login sheet, and
    /// their snapshot on disk for the *next* account to render on launch.
    func handleSessionExpiry() {
        state = .signedOut
        authError = .unauthorized
        Task {
            await onSignOut?()
            try? await CachedProfile.clear()
            await SnapshotCache.shared.clear()
        }
    }

    func updateProfile(_ me: Me) {
        state = .signedIn(me)
        Task { try? await CachedProfile.save(me) }
    }

    // MARK: - PKCE

    private static func randomVerifier() -> String {
        var bytes = [UInt8](repeating: 0, count: 64)
        _ = SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes)
        return Data(bytes).base64URLEncodedString()
    }

    private static func challenge(for verifier: String) -> String {
        Data(SHA256.hash(data: Data(verifier.utf8))).base64URLEncodedString()
    }
}

extension AuthController: ASWebAuthenticationPresentationContextProviding {
    nonisolated func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        MainActor.assumeIsolated {
            let scene = UIApplication.shared.connectedScenes
                .compactMap { $0 as? UIWindowScene }
                .first { $0.activationState == .foregroundActive }
            return scene?.keyWindow ?? ASPresentationAnchor()
        }
    }
}

extension Data {
    /// base64url, no padding — what RFC 7636 requires and what the server's
    /// ChallengeFromVerifier produces.
    func base64URLEncodedString() -> String {
        base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
}
