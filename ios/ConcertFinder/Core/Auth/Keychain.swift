import Foundation
import Security
import os

/// Keychain-backed storage for the session token.
///
/// Two attribute choices are deliberate (plan §5.4):
///
/// - `kSecAttrAccessibleAfterFirstUnlock`, not `WhenUnlocked`. Push handling
///   can run before the device has been unlocked in a given boot, and a token
///   that is unreadable then would make the app fail to fetch the event a
///   notification just deep-linked to.
/// - No iCloud synchronisation. A session is device-specific; syncing it
///   would put one device's credential on another, where the server would
///   still honour it.
actor KeychainTokenStore: TokenStore {
    private let service: String
    private let account = "session-token"

    /// Cached so the common path — attaching a bearer header to every request
    /// — does not hit the Keychain each time. The Keychain remains the source
    /// of truth; this is only ever populated from it.
    private var cached: String?
    private var didLoad = false

    init(service: String = "com.concertfinder.ph.session") {
        self.service = service
    }

    func currentToken() async -> String? {
        if !didLoad {
            cached = read()
            didLoad = true
        }
        return cached
    }

    /// Returns whether the token reached the Keychain.
    ///
    /// The status was discarded, and a discarded `SecItemAdd` failure is
    /// invisible in a specific and confusing way: the in-memory cache above
    /// makes the rest of *this* launch work perfectly, and the session is
    /// simply gone at the next one — which the user reads as being signed out
    /// overnight for no reason. `@discardableResult` because the honest
    /// remedy at the one call site is nothing (the sign-in has already
    /// succeeded and the token still works until relaunch); the log is what
    /// makes it diagnosable at all.
    @discardableResult
    func store(_ token: String) async -> Bool {
        cached = token
        didLoad = true
        var query = baseQuery()
        SecItemDelete(query as CFDictionary)
        query[kSecValueData as String] = Data(token.utf8)
        query[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlock
        let status = SecItemAdd(query as CFDictionary, nil)
        if status != errSecSuccess {
            Logger(subsystem: "com.concertfinder.ph", category: "keychain")
                .error("session token was not persisted: OSStatus \(status)")
            return false
        }
        return true
    }

    func clear() async {
        cached = nil
        didLoad = true
        SecItemDelete(baseQuery() as CFDictionary)
    }

    private func baseQuery() -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
    }

    private func read() -> String? {
        var query = baseQuery()
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne

        var item: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &item) == errSecSuccess,
              let data = item as? Data,
              let token = String(data: data, encoding: .utf8),
              !token.isEmpty
        else {
            return nil
        }
        return token
    }
}
