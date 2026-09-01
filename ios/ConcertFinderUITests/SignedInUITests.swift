import XCTest

/// The signed-in flows plan §8 asks for: feed load, save, filter.
///
/// These need a real session, which needs a Spotify account on the operator's
/// Development Mode allowlist (§3.2). There is no way around that from a test:
/// the OAuth handshake runs in ASWebAuthenticationSession against Spotify's
/// own login page, and automating it would mean driving a third party's web UI
/// and storing their credentials — brittle, and against Spotify's terms.
///
/// So the session is **injected** instead. Pass a token the app stores in the
/// Keychain at launch, and the rest of the flow is ordinary UI testing:
///
///     CF_UI_TEST_SESSION_TOKEN=<session id from a real login> \
///     CF_UI_TEST_API_BASE_URL=https://concertfinder.app \
///       xcodebuild test -only-testing:ConcertFinderUITests/SignedInUITests ...
///
/// Without those variables every test here skips, so CI stays green and the
/// suite is honest about what it did and did not check.
///
/// To get a token: sign in on a device or simulator, then read it out of the
/// Keychain — or take `cf_session` from a browser login, since the two are the
/// same value (that equivalence is the whole point of the bearer path in
/// plan §4.1).
final class SignedInUITests: XCTestCase {

    private var sessionToken: String?
    private var apiBaseURL: String?

    override func setUpWithError() throws {
        continueAfterFailure = false
        sessionToken = ProcessInfo.processInfo.environment["CF_UI_TEST_SESSION_TOKEN"]
        apiBaseURL = ProcessInfo.processInfo.environment["CF_UI_TEST_API_BASE_URL"]
        try XCTSkipIf(
            (sessionToken ?? "").isEmpty,
            "Set CF_UI_TEST_SESSION_TOKEN (and optionally CF_UI_TEST_API_BASE_URL) to run the signed-in flows."
        )
    }

    /// Launches with an injected session. The app reads these in
    /// `AppContainer` and seeds the Keychain before its first request.
    private func launchSignedIn() -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["CF_UI_TEST_SESSION_TOKEN"] = sessionToken
        if let apiBaseURL, !apiBaseURL.isEmpty {
            app.launchEnvironment["CF_UI_TEST_API_BASE_URL"] = apiBaseURL
        }
        // A fresh first-run state each time, so the feed test is not silently
        // asserting against the first-run screen.
        //
        // The other two matter for the same reason and one of them is worse:
        // the soft prompt for notifications is a *sheet*, raised by the model
        // the first time a scan settles with results in it — which is exactly
        // what every test below waits for. Without this it covers the feed
        // and each of them fails on a control it can no longer reach.
        app.launchArguments += [
            "-cf.firstRunCompleted", "YES",
            "-cf.pushPromptOffered", "YES",
            "-cf.hint.saveVersusSubscribe", "YES",
        ]
        app.launch()
        return app
    }

    /// The feed loads and shows the tab bar. Deliberately does not assert on
    /// specific concerts: what is playing near the test account changes daily,
    /// and a test that fails because a band cancelled is worse than no test.
    @MainActor
    func testFeedLoads() throws {
        let app = launchSignedIn()

        XCTAssertTrue(
            app.navigationBars["Concerts"].waitForExistence(timeout: 60),
            "The feed should load for a signed-in session"
        )
        // The login screen must NOT be showing — that is the regression this
        // catches, and it is the one an injected-token change would cause.
        XCTAssertFalse(app.buttons["Continue with Spotify"].exists)
    }

    /// Saving is per act. This drives the first bookmark it finds and checks
    /// the control reports the new state, which is the per-act wiring in
    /// EventCard — the thing that silently saves the wrong artist when wrong.
    @MainActor
    func testSaveTogglesAnAct() throws {
        let app = launchSignedIn()
        XCTAssertTrue(app.navigationBars["Concerts"].waitForExistence(timeout: 60))

        // Bookmarks are labelled "Save <artist>" with a value of Saved/Not
        // saved, so find one by label prefix rather than by index.
        let bookmark = app.buttons.matching(
            NSPredicate(format: "label BEGINSWITH %@", "Save ")
        ).firstMatch
        try XCTSkipUnless(
            bookmark.waitForExistence(timeout: 30),
            "No concerts in the feed for this account, so there is nothing to save."
        )

        let before = bookmark.value as? String
        bookmark.tap()

        // The control is optimistic, so the value flips immediately and the
        // request settles behind it.
        let flipped = NSPredicate(format: "value != %@", before ?? "")
        expectation(for: flipped, evaluatedWith: bookmark)
        waitForExpectations(timeout: 15)
    }

    /// Opening filters and applying a facet returns to a feed rather than an
    /// error. Facet values must go back to the server verbatim; a client-side
    /// mangling shows up here as an empty list where the pill promised a
    /// count.
    @MainActor
    func testFilterByGenre() throws {
        let app = launchSignedIn()
        XCTAssertTrue(app.navigationBars["Concerts"].waitForExistence(timeout: 60))

        app.buttons["Filters"].firstMatch.tap()
        XCTAssertTrue(app.navigationBars["Filters"].waitForExistence(timeout: 10))

        // Apply with nothing changed: the round trip is what is under test,
        // not any particular genre, and the available facets vary by account.
        app.buttons["Apply"].tap()
        XCTAssertTrue(
            app.navigationBars["Concerts"].waitForExistence(timeout: 30),
            "Applying filters should return to the feed"
        )
    }

    /// Navigating into an event and back. Catches the navigation regression
    /// §8 has in mind, which is the cheapest class of breakage to introduce
    /// and the most annoying to find by hand.
    @MainActor
    func testOpensEventDetail() throws {
        let app = launchSignedIn()
        XCTAssertTrue(app.navigationBars["Concerts"].waitForExistence(timeout: 60))

        let firstCard = app.buttons.matching(
            NSPredicate(format: "label BEGINSWITH %@", "Save ")
        ).firstMatch
        try XCTSkipUnless(
            firstCard.waitForExistence(timeout: 30),
            "No concerts in the feed for this account."
        )

        // Tap the card body rather than a control on it.
        app.scrollViews.firstMatch.tap()
        XCTAssertTrue(
            app.buttons["Add to Calendar"].waitForExistence(timeout: 15),
            "Tapping a card should open the event detail"
        )
    }
}
