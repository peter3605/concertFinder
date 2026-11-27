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

    /// The signed-in account, if there is one.
    var me: Me? {
        if case .signedIn(let me) = state { return me }
        return nil
    }

    /// Whether the signed-in account has a Spotify identity. Decides the
    /// "Powered by Spotify" attribution and which artist search to use;
    /// false when signed out, where no Spotify-derived data is on screen.
    var hasSpotify: Bool { me?.hasSpotify ?? false }
    private(set) var authError: APIError?
    /// Whether this deployment gates NEW accounts on an invite code, which
    /// decides only whether the sign-in screen offers a text box.
    ///
    /// Defaults to false and stays false if site-info cannot be reached, for
    /// the same reason isBuildTooOld() fails open: a blip must not add a
    /// mandatory-looking field to the sign-in screen of a deployment that
    /// does not have a gate. Getting it wrong in this direction costs a user
    /// with a code one extra round trip, since the callback refuses the
    /// signup and says exactly what is missing.
    private(set) var inviteRequired = false
    /// Whether this deployment mounts Sign in with Apple (`apple_sign_in` in
    /// site-info). False until site-info answers, for the same reason as
    /// `inviteRequired`: a button that leads to a 501 is worse than none.
    private(set) var appleSignInAvailable = false
    /// Whether a new Spotify account could still be created. Spotify stays
    /// at five accounts, so the create-account tab hides that path unless a
    /// place is free; the sign-in tab always offers it to returning users.
    private(set) var spotifySignupsOpen = false
    /// Asks the server whether `code` is a Spotify invite. Any failure reads
    /// as "no": the cost is a hidden button for someone who could have used
    /// it, while the other direction sends them through Spotify's consent
    /// screen to be refused. The callback checks the kind again either way.
    func isSpotifyInvite(_ code: String) async -> Bool {
        let trimmed = code.trimmingCharacters(in: .whitespacesAndNewlines)
        guard inviteRequired, !trimmed.isEmpty else { return false }
        return (try? await api.checkInvite(trimmed))?.admitsSpotify ?? false
    }

    /// An email sign-in this app started and has not finished. Drives the
    /// "enter the code" step, and outlives the process (see
    /// PendingEmailSignIn) because the link arrives while the app is away.
    private(set) var pendingEmail: PendingEmailSignIn?
    /// A one-line outcome that is not an error, such as "address confirmed"
    /// after a verification link opened the app. Cleared by the view.
    var notice: String?

    /// The raw nonce for the Apple request in flight. Apple sees only its
    /// SHA-256; the server needs the raw value to check the token's claim.
    private var appleNonce: String?

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
        // One site-info fetch answers both questions, so they share a call
        // rather than each making their own on the launch path.
        let info = try? await api.siteInfo()
        if isBuildTooOld(info) {
            state = .updateRequired
            return
        }
        inviteRequired = info?.inviteRequired ?? false
        appleSignInAvailable = info?.appleSignIn ?? false
        spotifySignupsOpen = info?.spotifySignupsOpen ?? false
        pendingEmail = PendingEmailSignInStore.load()
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

    private func isBuildTooOld(_ info: SiteInfo?) -> Bool {
        guard let info, let floor = info.minIosBuild, floor > 0 else {
            // No floor published, or the check itself failed. Failing open is
            // right: a site-info blip must not brick every installed copy.
            return false
        }
        return APIClient.currentBuild() < floor
    }

    // MARK: - Sign in

    /// Runs the full login: PKCE against us, PKCE against Spotify inside the
    /// web context, then a one-time code exchanged for the session.
    func signIn(inviteCode: String = "") async {
        authError = nil

        // Our half of the handshake. The verifier never leaves the device,
        // which is what makes an intercepted callback URL useless.
        let verifier = Self.randomVerifier()
        let challenge = Self.challenge(for: verifier)

        var components = URLComponents(url: baseURL.appendingPathComponent("api/auth/login"),
                                       resolvingAgainstBaseURL: false)
        var query = [
            URLQueryItem(name: "client", value: "ios"),
            URLQueryItem(name: "app_challenge", value: challenge),
        ]
        // Only sent when the person typed something. An empty `invite=` is
        // not the same as no parameter to a server deciding whether a signup
        // presented a code, and sending one would be a lie about what the
        // user did. The server normalizes case and spacing; trimming here
        // just keeps a stray space out of the URL.
        let invite = inviteCode.trimmingCharacters(in: .whitespacesAndNewlines)
        if !invite.isEmpty {
            query.append(URLQueryItem(name: "invite", value: invite))
        }
        components?.queryItems = query
        guard let loginURL = components?.url else {
            authError = .unknown("Could not build the sign-in URL.")
            return
        }

        do {
            let callbackURL = try await presentWebAuth(startingAt: loginURL)
            switch Self.callbackOutcome(callbackURL) {
            case .code(let code):
                let result = try await api.exchangeMobileCode(code: code, verifier: verifier)
                await finishSignIn(result)
            case .refused(let reason):
                // The server turned the sign-in down and said why. Nothing
                // was minted, so there is nothing to exchange.
                authError = .unknown(Self.refusalMessage(reason))
            case .empty:
                authError = .unknown("Sign-in finished without a code. Please try again.")
            }
        } catch let error as APIError {
            authError = error
        } catch is CancellationError {
            // The user dismissed the sheet. Not an error worth showing.
        } catch {
            authError = .unknown(error.localizedDescription)
        }
    }

    /// Every sign-in path ends here, whichever provider it went through: the
    /// Spotify exchange, Apple, the email link and the email code all answer
    /// with the same body (`writeBearerSession` on the server).
    private func finishSignIn(_ result: MobileExchangeResponse) async {
        await tokens.store(result.sessionToken)
        try? await CachedProfile.save(result.user)
        // Whatever email sign-in was pending is moot now, and its verifier
        // must not linger for a later link to be checked against.
        clearPendingEmail()
        authError = nil
        state = .signedIn(result.user)
        onSignIn?(result.user)
    }

    // MARK: - Sign in with Apple

    /// Fills in the request the system button is about to send. Name and
    /// email are asked for because Apple sends them on the first
    /// authorization only; the server stores them from that one response.
    func prepare(_ request: ASAuthorizationAppleIDRequest) {
        authError = nil
        let raw = Self.randomVerifier()
        appleNonce = raw
        request.requestedScopes = [.fullName, .email]
        request.nonce = Self.sha256Hex(raw)
    }

    func completeAppleSignIn(_ result: Result<ASAuthorization, Error>, inviteCode: String = "") async {
        let nonce = appleNonce
        appleNonce = nil
        switch result {
        case .failure(let error):
            // Dismissing the sheet is not an error worth showing.
            if (error as? ASAuthorizationError)?.code == .canceled { return }
            authError = .unknown("Sign in with Apple didn't finish. Please try again.")
        case .success(let authorization):
            guard let credential = authorization.credential as? ASAuthorizationAppleIDCredential,
                  let tokenData = credential.identityToken,
                  let identityToken = String(data: tokenData, encoding: .utf8),
                  let codeData = credential.authorizationCode,
                  let code = String(data: codeData, encoding: .utf8),
                  let nonce
            else {
                authError = .unknown("Apple didn't return a usable sign-in. Please try again.")
                return
            }
            do {
                let response = try await api.signInWithApple(
                    identityToken: identityToken,
                    authorizationCode: code,
                    nonce: nonce,
                    givenName: credential.fullName?.givenName,
                    familyName: credential.fullName?.familyName,
                    invite: Self.trimmedInvite(inviteCode)
                )
                await finishSignIn(response)
            } catch let error as APIError {
                authError = Self.signInError(error)
            } catch is CancellationError {
            } catch {
                authError = .unknown(error.localizedDescription)
            }
        }
    }

    // MARK: - Email sign-in

    /// Asks the server to mail a link and a code. Returns whether the request
    /// was accepted, which says nothing about whether the address has an
    /// account: the server answers 202 either way, on purpose.
    @discardableResult
    func startEmailSignIn(email: String, inviteCode: String = "") async -> Bool {
        authError = nil
        let address = email.trimmingCharacters(in: .whitespacesAndNewlines)
        let verifier = Self.randomVerifier()
        let invite = Self.trimmedInvite(inviteCode)
        do {
            try await api.startEmailSignIn(
                email: address,
                invite: invite,
                appChallenge: Self.challenge(for: verifier)
            )
            let pending = PendingEmailSignIn(email: address, verifier: verifier, startedAt: Date(), invite: invite)
            PendingEmailSignInStore.save(pending)
            pendingEmail = pending
            return true
        } catch let error as APIError {
            authError = Self.signInError(error)
        } catch {
            authError = .unknown(error.localizedDescription)
        }
        return false
    }

    /// The 6-digit code from the mail, for when the link opened somewhere
    /// else -- another device, or Safari.
    func submitEmailCode(_ code: String) async {
        guard let pending = pendingEmail ?? PendingEmailSignInStore.load() else {
            authError = .unknown("That sign-in has expired. Ask for a new email.")
            return
        }
        authError = nil
        do {
            let response = try await api.signInWithEmailCode(
                email: pending.email,
                code: code.trimmingCharacters(in: .whitespacesAndNewlines),
                verifier: pending.verifier
            )
            await finishSignIn(response)
        } catch let error as APIError {
            authError = Self.signInError(error)
        } catch {
            authError = .unknown(error.localizedDescription)
        }
    }

    /// The universal link from the mail opened the app.
    ///
    /// Two kinds of link land here. A sign-in link is bound to the app that
    /// asked for it by the verifier, so it only works if this install started
    /// it. A confirmation link (adding an address, from Settings on either
    /// client) needs no verifier and no session: using it is the proof.
    func handleEmailLink(token: String) async {
        let pending = PendingEmailSignInStore.load()
        do {
            switch try await api.consumeEmailLink(token: token, verifier: pending?.verifier) {
            case .signedIn(let response):
                await finishSignIn(response)
            case .addressVerified:
                notice = "Email address confirmed."
                if case .signedIn = state, let me = try? await api.me() {
                    updateProfile(me)
                }
            }
        } catch APIError.server(400, _) where pending == nil {
            // A link this install never asked for -- most often one requested
            // from the website and opened on a phone with the app installed,
            // where iOS hands every such link to us. The server refuses it
            // without our verifier, and it is not burned by that refusal.
            authError = .unknown("This sign-in link was requested from a browser, so it can't sign in this app. Use the 6-digit code from the same email where you asked for it, or sign in here with your email address.")
        } catch let error as APIError {
            authError = Self.signInError(error)
        } catch {
            authError = .unknown(error.localizedDescription)
        }
    }

    /// "Use a different address" on the code step.
    func cancelEmailSignIn() {
        clearPendingEmail()
        authError = nil
    }

    private func clearPendingEmail() {
        PendingEmailSignInStore.clear()
        pendingEmail = nil
    }

    // MARK: - Errors

    /// Server refusals worded for the sign-in screen. The email routes answer
    /// with bare codes for the invite cases (`invite_required`), which are
    /// for clients to translate rather than to show.
    nonisolated static func signInError(_ error: APIError) -> APIError {
        guard case .server(let status, let message) = error else { return error }
        switch (status, message) {
        case (403, "invite_required"?):
            return .unknown("ConcertFinder is invite-only at the moment, so a new account needs an invite code.")
        case (403, "invite_invalid"?):
            return .unknown("That invite code is not valid. It may have already been used, or it may have expired.")
        case (400, "invalid or expired code"?):
            return .unknown("That code is wrong or has expired. Check the email, or ask for a new one.")
        case (429, _):
            return .unknown("Too many sign-in attempts. Wait a few minutes and try again.")
        default:
            return error
        }
    }

    /// What the universal link that ended the auth sheet carries.
    enum CallbackOutcome: Equatable {
        /// A one-time code to exchange at /api/auth/mobile/exchange.
        case code(String)
        /// A refusal code from the server's `refuse()` (CF-44).
        case refused(String)
        /// Neither, which the server never sends.
        case empty
    }

    /// Reads the callback URL. `error` is checked first and wins: a refusal
    /// must never reach the exchange, and must not be mistaken for the user
    /// dismissing the sheet, which is what the old bare 403 page turned
    /// every refusal into.
    nonisolated static func callbackOutcome(_ url: URL) -> CallbackOutcome {
        let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems ?? []
        func value(_ name: String) -> String? {
            guard let raw = items.first(where: { $0.name == name })?.value,
                  !raw.isEmpty else { return nil }
            return raw
        }
        if let error = value("error") { return .refused(error) }
        if let code = value("code") { return .code(code) }
        return .empty
    }

    /// The sign-in screen's wording for each refusal code. The codes are the
    /// server's `LoginError*` constants, and the wording follows the web's
    /// REFUSALS map in web/src/pages/login.tsx so the two clients say the
    /// same thing. An unknown code gets a generic line rather than the raw
    /// code: builds on phones outlive the server's list of codes.
    nonisolated static func refusalMessage(_ code: String) -> String {
        switch code {
        case "invite_required":
            "Creating an account needs an invite code while ConcertFinder is in early access. Enter yours and try again. Already have an account? Accounts never need a code."
        case "invite_invalid":
            "That invite code isn't valid. It may have been used up or expired. Check it for typos, or ask whoever sent it for another."
        case "invite_not_spotify":
            "Spotify sign-in is limited, and that invite is for creating an account with email or Sign in with Apple. It hasn't been used, so you can use it for either. Spotify needs a Spotify invite, which is only for people the operator has added on Spotify's side."
        case "spotify_full":
            "Spotify sign-in is limited, and ConcertFinder can't take any more Spotify accounts right now. Your invite code hasn't been used, so you can create your account with email or Sign in with Apple instead."
        case "spotify_not_registered":
            "Spotify sign-in is limited to accounts the operator has added. Your invite code hasn't been used, so you can create your account with email or Sign in with Apple instead."
        default:
            "Sign-in didn't complete. Please try again."
        }
    }

    private static func trimmedInvite(_ raw: String) -> String? {
        let invite = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        return invite.isEmpty ? nil : invite
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
        clearPendingEmail()
        // A different account on this device gets its own first run, and its
        // own one-off hints.
        FirstRunTracker.reset()
        HintStore.reset()
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

    /// Lowercase hex, which is what the server's `apple.HashNonce` compares
    /// the token's nonce claim against. Not base64url like the PKCE
    /// challenge: the two hashes travel to different places.
    nonisolated static func sha256Hex(_ value: String) -> String {
        SHA256.hash(data: Data(value.utf8)).map { String(format: "%02x", $0) }.joined()
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
