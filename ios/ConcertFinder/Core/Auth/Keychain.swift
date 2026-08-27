import Foundation
import Security

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

    func store(_ token: String) async {
        cached = token
        didLoad = true
        var query = baseQuery()
        SecItemDelete(query as CFDictionary)
        query[kSecValueData as String] = Data(token.utf8)
        query[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlock
        SecItemAdd(query as CFDictionary, nil)
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
