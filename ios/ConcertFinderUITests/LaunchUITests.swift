import XCTest

/// Navigation smoke tests.
///
/// Scoped deliberately to what is true with **no backend reachable**, which
/// is the situation until the deploy in plan M0 lands. That still covers the
/// regression these exist to catch — the app launching into a broken or blank
/// root view — without pretending to test flows that need a live API.
///
/// The signed-in flows §8 lists (feed load, save, filter) need a server and a
/// Spotify account on the Development Mode allowlist. They belong here once
/// there is an environment to point at; adding them now would produce tests
/// that fail for reasons unrelated to the code.
final class LaunchUITests: XCTestCase {

    override func setUp() {
        super.setUp()
        continueAfterFailure = false
    }

    /// With no session and no reachable server, the app must settle on the
    /// login screen rather than a spinner that never resolves. The restore
    /// path has a failure branch for exactly this, and a regression in it
    /// would leave every cold launch hanging.
    @MainActor
    func testLaunchesToSignedOutState() {
        let app = XCUIApplication()
        app.launch()

        let signInButton = app.buttons["Continue with Spotify"]
        XCTAssertTrue(
            signInButton.waitForExistence(timeout: 60),
            "The app should settle on the login screen when there is no session"
        )
    }

    /// The attribution is required on every surface showing Spotify-derived
    /// data, and it is a compliance item rather than a design preference —
    /// worth a test so it cannot quietly disappear from the first screen a
    /// reviewer sees.
    @MainActor
    func testLoginScreenShowsSpotifyAttribution() {
        let app = XCUIApplication()
        app.launch()

        XCTAssertTrue(
            app.staticTexts["Powered by Spotify"].waitForExistence(timeout: 60),
            "The login screen must carry the Spotify attribution"
        )
    }

    /// The privacy claim is load-bearing for the App Store submission, so the
    /// copy asserting it should not be edited away by accident.
    @MainActor
    func testLoginScreenStatesThePrivacyPosition() {
        let app = XCUIApplication()
        app.launch()

        let predicate = NSPredicate(format: "label CONTAINS[c] %@", "listening history is never stored")
        XCTAssertTrue(
            app.staticTexts.containing(predicate).firstMatch.waitForExistence(timeout: 60),
            "The login screen should state that listening history is not stored"
        )
    }
}
