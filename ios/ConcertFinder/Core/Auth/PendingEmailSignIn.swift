import Foundation
import Security
import os

/// An email sign-in this app started and has not finished: the address it was
/// sent to and the PKCE verifier whose challenge the server stored with the
/// token.
///
/// It has to outlive the process. The link arrives in Mail, and by the time
/// someone has switched apps, read the message and tapped it, iOS may well
/// have killed this one -- a verifier held only in memory would turn every
/// such link into "this link was not requested from this app". The Keychain
/// rather than UserDefaults because the verifier is half a credential: with
/// the token from the mail it signs the account in.
///
/// The server's tokens live 15 minutes (`email_login_tokens`), so anything
/// older than `lifetime` is treated as gone rather than offered for a code the
/// server will refuse.
struct PendingEmailSignIn: Codable, Equatable, Sendable {
    let email: String
    let verifier: String
    let startedAt: Date
    /// The invite code it was started with, so "send again" on a signup
    /// sends again: for an unknown address with no code the server mails
    /// nothing, and the resend would silently go nowhere.
    var invite: String? = nil

    /// A little past the server's 15 minutes, so clock skew never retires a
    /// pending sign-in the server would still honour.
    static let lifetime: TimeInterval = 20 * 60

    func isExpired(now: Date = Date()) -> Bool {
        now.timeIntervalSince(startedAt) > Self.lifetime
    }
}

/// Keychain storage for the one pending email sign-in. One at a time on
/// purpose: starting another replaces it, which matches the server, where the
/// newest code is the one the person is reading.
enum PendingEmailSignInStore {
    private static let service = "com.concertfinder.ph.email-signin"
    private static let account = "pending"

    static func load(now: Date = Date()) -> PendingEmailSignIn? {
        var query = baseQuery()
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var item: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &item)
        if status != errSecSuccess && status != errSecItemNotFound {
            log.error("pending email sign-in unreadable: OSStatus \(status)")
        }
        guard status == errSecSuccess,
              let data = item as? Data,
              let pending = try? JSONDecoder().decode(PendingEmailSignIn.self, from: data)
        else { return nil }
        if pending.isExpired(now: now) {
            clear()
            return nil
        }
        return pending
    }

    static func save(_ pending: PendingEmailSignIn) {
        guard let data = try? JSONEncoder().encode(pending) else { return }
        var query = baseQuery()
        SecItemDelete(query as CFDictionary)
        query[kSecValueData as String] = data
        // ThisDeviceOnly: the verifier belongs to this install's sign-in and
        // must not reach another device through a backup restore.
        query[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        let status = SecItemAdd(query as CFDictionary, nil)
        // Logged for the reason KeychainTokenStore logs: a failed write is
        // invisible until the next launch, where it reads as a link that
        // "was not requested from this app".
        if status != errSecSuccess {
            log.error("pending email sign-in not persisted: OSStatus \(status)")
        }
    }

    private static let log = Logger(subsystem: "com.concertfinder.ph", category: "keychain")

    static func clear() {
        SecItemDelete(baseQuery() as CFDictionary)
    }

    private static func baseQuery() -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
    }
}

/// Where an email link points, if it is one of ours.
///
/// The sign-in mail's link is `https://<site>/app/auth/email?token=...`,
/// listed in apple-app-site-association so iOS hands it to the app. The host
/// is checked against the API origin so a lookalike domain's link is not
/// treated as a sign-in.
enum EmailLink {
    static let path = "/app/auth/email"

    static func token(from url: URL, expectedHost: String?) -> String? {
        guard let components = URLComponents(url: url, resolvingAgainstBaseURL: false),
              components.scheme == "https",
              components.path == path
        else { return nil }
        if let expectedHost, components.host?.lowercased() != expectedHost.lowercased() {
            return nil
        }
        guard let token = components.queryItems?.first(where: { $0.name == "token" })?.value,
              !token.isEmpty
        else { return nil }
        return token
    }
}
