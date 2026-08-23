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
    private var environment: String {
        #if DEBUG
        "sandbox"
        #else
        "production"
        #endif
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
        try? await api.deregisterDevice(token: token)
        deviceToken = nil
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
