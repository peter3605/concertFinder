import Foundation
import Testing

@testable import ConcertFinder

/// Assertions about the shipped bundle rather than about code.
///
/// Both of these are things no other test can see and no local run reports.
/// A privacy manifest that stops being copied still builds, still installs,
/// still passes every other test here, and fails only once App Store Connect
/// has already accepted the upload -- as an ITMS-91053 email, hours later,
/// with the build never reaching TestFlight. The orientation keys are the
/// same shape of problem one step earlier, at validation.
///
/// These run against `Bundle.main`, which is the app under test because the
/// test bundle is hosted (`TEST_HOST`), so what is inspected is the real
/// built product and not the source file.
struct AppBundleTests {

    /// The shipped `Info.plist`, parsed from the file rather than read through
    /// `Bundle.main.infoDictionary`.
    ///
    /// Not interchangeable: `infoDictionary` hands back the values *resolved*
    /// for the running device, so a device-suffixed key like
    /// `UISupportedInterfaceOrientations~ipad` is absent from it entirely on an
    /// iPhone -- which is where these tests run. Reading the file asserts what
    /// is actually in the bundle, on any destination.
    private func infoPlist() throws -> [String: Any] {
        let url = try #require(Bundle.main.url(forResource: "Info", withExtension: "plist"))
        let data = try Data(contentsOf: url)
        let plist = try PropertyListSerialization.propertyList(from: data, format: nil)
        return try #require(plist as? [String: Any])
    }

    private func privacyManifest() throws -> [String: Any] {
        let url = try #require(
            Bundle.main.url(forResource: "PrivacyInfo", withExtension: "xcprivacy"),
            "PrivacyInfo.xcprivacy is missing from the app bundle. Apple requires it at the bundle root; without it the upload is accepted and then rejected as ITMS-91053."
        )
        let data = try Data(contentsOf: url)
        let plist = try PropertyListSerialization.propertyList(from: data, format: nil)
        return try #require(plist as? [String: Any], "PrivacyInfo.xcprivacy is not a dictionary")
    }

    /// The mandatory half. `UserDefaults` is a required-reason API and the app
    /// uses it in `ThemeStore` and `SnapshotCache`, so the category must be
    /// declared with a reason -- an empty or absent reasons array is treated
    /// as no declaration at all.
    @Test func manifestDeclaresTheUserDefaultsCategory() throws {
        let manifest = try privacyManifest()
        let types = manifest["NSPrivacyAccessedAPITypes"] as? [[String: Any]] ?? []

        let userDefaults = types.first {
            $0["NSPrivacyAccessedAPIType"] as? String == "NSPrivacyAccessedAPICategoryUserDefaults"
        }
        let entry = try #require(
            userDefaults,
            "The app reads and writes UserDefaults, which is a required-reason API, so the manifest must declare NSPrivacyAccessedAPICategoryUserDefaults."
        )

        let reasons = entry["NSPrivacyAccessedAPITypeReasons"] as? [String] ?? []
        // CA92.1 is "access info from same app, per documentation" -- the
        // app's own configuration and state. The app-group reasons do not
        // apply while there is no app group and no extension; adding either
        // means revisiting this.
        #expect(reasons.contains("CA92.1"))
    }

    /// Declared because it must agree with the App Store Connect privacy
    /// labels, and because the interesting claim is a negative one: the
    /// Spotify listening data the whole feed is derived from is never
    /// persisted, so it appears nowhere in this list. A future change that
    /// starts storing it has to touch this test.
    @Test func manifestClaimsNoTrackingAndOnlyFunctionalCollection() throws {
        let manifest = try privacyManifest()

        #expect(manifest["NSPrivacyTracking"] as? Bool == false)
        #expect((manifest["NSPrivacyTrackingDomains"] as? [String] ?? []).isEmpty)

        let collected = manifest["NSPrivacyCollectedDataTypes"] as? [[String: Any]] ?? []
        #expect(!collected.isEmpty, "The app does collect identity, email, location and a device token.")
        for entry in collected {
            let type = entry["NSPrivacyCollectedDataType"] as? String ?? "?"
            #expect(entry["NSPrivacyCollectedDataTypeTracking"] as? Bool == false, "\(type) is marked as tracking")
            let purposes = entry["NSPrivacyCollectedDataTypePurposes"] as? [String] ?? []
            #expect(
                purposes == ["NSPrivacyCollectedDataTypePurposeAppFunctionality"],
                "\(type) declares a purpose beyond app functionality, which the privacy labels do not claim"
            )
        }
    }

    /// iPad ships (`TARGETED_DEVICE_FAMILY` is "1,2") and the app does not
    /// claim `UIRequiresFullScreen`, so all four orientations are required or
    /// validation objects. The iPhone list is asserted separately to keep the
    /// two keys from being collapsed into one -- that silences the warning by
    /// letting an iPhone sit upside down, which is not the intent.
    @Test func iPadSupportsEveryOrientationAndIPhoneDoesNot() throws {
        let infoPlist = try infoPlist()
        #expect(infoPlist["UIRequiresFullScreen"] == nil)

        let iPad = Set(infoPlist["UISupportedInterfaceOrientations~ipad"] as? [String] ?? [])
        #expect(iPad == [
            "UIInterfaceOrientationPortrait",
            "UIInterfaceOrientationPortraitUpsideDown",
            "UIInterfaceOrientationLandscapeLeft",
            "UIInterfaceOrientationLandscapeRight",
        ])

        let iPhone = Set(infoPlist["UISupportedInterfaceOrientations"] as? [String] ?? [])
        #expect(!iPhone.contains("UIInterfaceOrientationPortraitUpsideDown"))
        #expect(iPhone.contains("UIInterfaceOrientationPortrait"))
    }
}
