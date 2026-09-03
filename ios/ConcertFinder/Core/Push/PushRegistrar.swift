import Observation
import UIKit
import UserNotifications

/// Owns APNs registration and the server-side device record.
@MainActor
@Observable
final class PushRegistrar {
    private(set) var isAuthorized = false
    private(set) var deviceToken: String?

    private let api: APIClient

    /// Which APNs host the server should send to for this build. It must
    /// match the aps-environment entitlement: a token minted by a debug or
    /// TestFlight build is rejected by the production host as
    /// BadDeviceToken, and a production token sent to sandbox reaches nobody
    /// while reporting success.
    ///
    /// Read from the embedded provisioning profile rather than inferred from
    /// the build configuration. `#if DEBUG` is not the same question and gets
    /// the wrong answer on the path this app actually uses: a build installed
    /// on a device has to be Release, because Debug bakes `127.0.0.1:3000`
    /// into Info.plist and cannot reach the API from a phone -- so every real
    /// device build reported "production" while carrying a `development`
    /// entitlement, and the token was a sandbox token wearing the wrong label.
    private var environment: String { Self.apsEnvironment() }

    /// The `aps-environment` entitlement, mapped to the server's vocabulary.
    ///
    /// An App Store build has no embedded profile, and that absence is itself
    /// the answer: App Store distribution is production. Every other failure
    /// to read one lands on the same default, which is the safe direction --
    /// the server skips a mismatched device and logs it, whereas guessing
    /// sandbox on a production build would look like delivery that never
    /// arrives.
    static func apsEnvironment() -> String {
        guard let url = Bundle.main.url(forResource: "embedded", withExtension: "mobileprovision"),
              let data = try? Data(contentsOf: url),
              // The profile is CMS-signed with the plist embedded as plain
              // text; isoLatin1 round-trips arbitrary bytes so the binary
              // wrapper cannot fail the decode.
              let text = String(data: data, encoding: .isoLatin1),
              let start = text.range(of: "<?xml"),
              let end = text.range(of: "</plist>"),
              let plistData = String(text[start.lowerBound..<end.upperBound]).data(using: .isoLatin1),
              let plist = try? PropertyListSerialization.propertyList(
                  from: plistData, format: nil
              ) as? [String: Any],
              let entitlements = plist["Entitlements"] as? [String: Any],
              let aps = entitlements["aps-environment"] as? String
        else {
            return "production"
        }
        // Apple spells it "development"; user_devices.environment and
        // APNS_ENVIRONMENT both spell it "sandbox".
        return aps == "development" ? "sandbox" : "production"
    }

    init(api: APIClient) {
        self.api = api
    }

    /// Asks for permission and, if granted, registers with APNs.
    ///
    /// Called from the Settings toggle — deliberately *not* on launch.
    /// Guideline 4.5.4 aside, a permission prompt before the user has seen a
    /// single concert is a prompt they decline, and the decline is sticky.
    func requestAuthorizationAndRegister() async -> Bool {
        do {
            let granted = try await UNUserNotificationCenter.current()
                .requestAuthorization(options: [.alert, .sound, .badge])
            isAuthorized = granted
            guard granted else { return false }
            UIApplication.shared.registerForRemoteNotifications()
            return true
        } catch {
            isAuthorized = false
            return false
        }
    }

    /// Both halves of turning push on: the OS grant and the server-side
    /// preference. Returns whether it ended up on.
    ///
    /// Settings does the same two steps around its toggle and has an
    /// `APIClient` to hand; the soft prompt on the feed does not, and a grant
    /// without the preference is a device registered to receive notifications
    /// the sender will never select it for.
    func enable() async -> Bool {
        guard await requestAuthorizationAndRegister() else { return false }
        // Best effort. A failure here leaves the grant in place and the
        // preference off, which is the recoverable direction — the Settings
        // toggle is still the way to fix it.
        try? await api.updatePreferences(push: true)
        return true
    }

    /// Re-registers an existing grant on launch.
    ///
    /// APNs rotates tokens silently, so this runs on *every* launch rather
    /// than only after the first grant — a stale token fails as
    /// BadDeviceToken and the user simply stops receiving notifications with
    /// nothing to indicate why.
    func refreshRegistrationIfAuthorized() async {
        let settings = await UNUserNotificationCenter.current().notificationSettings()
        isAuthorized = settings.authorizationStatus == .authorized
        guard isAuthorized else { return }
        UIApplication.shared.registerForRemoteNotifications()
    }

    /// Called from the app delegate once APNs hands over a token.
    func didRegister(tokenData: Data) async {
        let token = tokenData.map { String(format: "%02x", $0) }.joined()
        deviceToken = token
        do {
            try await api.registerDevice(token: token, environment: environment)
        } catch {
            // Not fatal and not worth interrupting anyone over: the next
            // launch retries, and the only cost is notifications until then.
        }
    }

    /// Drops the server-side record. Called on sign-out and before account
    /// deletion, while the session still authorises the call.
    func deregister() async {
        guard let token = deviceToken else { return }
        // Cleared before the call, not after. On the session-expiry path this
        // request 401s, which re-enters the sign-out handler, which lands back
        // here — and with the token still set that is a second identical
        // request against a session already known to be gone.
        deviceToken = nil
        try? await api.deregisterDevice(token: token)
    }
}

/// What a tapped notification asks the app to show.
///
/// The payload carries keys rather than the event itself — APNs caps at 4KB
/// and a festival card is not small — so this is resolved against the feed
/// the app already has.
struct PushDeepLink: Equatable {
    let eventKey: String

    init?(userInfo: [AnyHashable: Any]) {
        guard let eventKey = userInfo["event_key"] as? String, !eventKey.isEmpty else {
            return nil
        }
        self.eventKey = eventKey
    }
}
