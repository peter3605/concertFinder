import XCTest

/// The email path end to end: sign-in screen to a populated feed for an
/// account with no Spotify (CF-37).
///
/// Two halves, because the code arrives by email and a UI test cannot read a
/// mailbox. Run the first, read the code from wherever the server delivered
/// it (the log, with `EMAIL_DELIVERY_MODE=log`), then run the second. The
/// pending sign-in is in the Keychain, so the second launch picks it up --
/// which is also the check that a mail read after the app was killed still
/// signs in.
///
///     TEST_RUNNER_CF_UI_TEST_API_BASE_URL=http://127.0.0.1:8080 \
///     TEST_RUNNER_CF_UI_TEST_WALK_EMAIL=walk@example.com \
///       xcodebuild test -only-testing:ConcertFinderUITests/EmailWalkUITests/testStep1RequestsCode ...
///
///     xcrun simctl location <device> set 40.7128,-74.0060
///     TEST_RUNNER_CF_UI_TEST_API_BASE_URL=http://127.0.0.1:8080 \
///     TEST_RUNNER_CF_UI_TEST_WALK_CODE=123456 \
///       xcodebuild test -only-testing:ConcertFinderUITests/EmailWalkUITests/testStep2SignsInAndReachesFeed ...
///
/// Without those variables both skip, so CI is not asserting on a server it
/// does not have.
///
/// Two things that look like app bugs and are not. Run with
/// `-parallel-testing-enabled NO`: parallel runs use a throwaway clone of
/// the simulator, so nothing survives between the halves. And sign the build
/// (`CODE_SIGN_IDENTITY=-` is enough on a simulator): with
/// `CODE_SIGNING_ALLOWED=NO` the app has no entitlements, every Keychain call
/// fails with errSecMissingEntitlement (-34018), and the pending sign-in --
/// like the session token -- lasts only until the process exits.
final class EmailWalkUITests: XCTestCase {

    private var env: [String: String] { ProcessInfo.processInfo.environment }

    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    private func launch() -> XCUIApplication {
        let app = XCUIApplication()
        if let base = env["CF_UI_TEST_API_BASE_URL"], !base.isEmpty {
            app.launchEnvironment["CF_UI_TEST_API_BASE_URL"] = base
        }
        // The soft push prompt is a sheet raised when the first scan settles
        // with results -- exactly when step 2 looks for the feed.
        app.launchArguments += ["-cf.pushPromptOffered", "YES", "-cf.hint.saveVersusSubscribe", "YES"]
        app.launch()
        return app
    }

    @MainActor
    func testStep1RequestsCode() throws {
        guard let email = env["CF_UI_TEST_WALK_EMAIL"], !email.isEmpty else {
            throw XCTSkip("Set CF_UI_TEST_WALK_EMAIL to run the email walk")
        }
        let app = launch()

        // A sign-in left pending by an earlier run comes back as the code
        // step; start over from the address.
        let different = app.buttons["Use a different address"]
        if different.waitForExistence(timeout: 5) { different.tap() }

        let field = app.textFields["email-address"]
        XCTAssertTrue(field.waitForExistence(timeout: 30))
        field.tap()
        field.typeText(email)
        app.buttons["email-send"].tap()

        XCTAssertTrue(app.textFields["email-code"].waitForExistence(timeout: 30),
                      "After sending, the code step should show")
        attachScreenshot(app, "1-code-step")
    }

    @MainActor
    func testStep2SignsInAndReachesFeed() throws {
        guard let code = env["CF_UI_TEST_WALK_CODE"], !code.isEmpty else {
            throw XCTSkip("Set CF_UI_TEST_WALK_CODE to finish the email walk")
        }
        let app = launch()

        // Each phase runs only if the app is on it, so a run that stopped
        // part-way resumes from where the account is rather than needing a
        // fresh code -- the server remembers every step already answered.
        let codeField = app.textFields["email-code"]
        let name = app.textFields["welcome-name"]
        let useLocation = app.buttons["Use current location"]
        let rock = app.buttons["Rock"]
        let event = app.descendants(matching: .any)["feed-event"].firstMatch

        // Relaunched: the pending sign-in must have survived in the Keychain.
        if codeField.waitForExistence(timeout: 15) {
            codeField.tap()
            codeField.typeText(code)
            app.buttons["email-code-submit"].tap()
        }

        // Welcome: name. The keyboard's continue key submits; the footer
        // button is hidden while the keyboard is up, so it cannot cover rows.
        if name.waitForExistence(timeout: 20) {
            attachScreenshot(app, "2-welcome-name")
            name.tap()
            name.typeText("Walk Tester\n")
        }

        // Location: the device's, not a typed city. A typed city is geocoded
        // by the server through Nominatim, which refuses a placeholder
        // contact in the User-Agent -- so on a local stack it is the server
        // that fails, not the app. Set the simulator's location first
        // (`xcrun simctl location <device> set 40.7128,-74.0060`).
        if useLocation.waitForExistence(timeout: 20) {
            useLocation.tap()
            let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
            for label in ["Allow While Using App", "Allow Once"] {
                let allow = springboard.buttons[label]
                if allow.waitForExistence(timeout: 5) { allow.tap(); break }
            }
            let next = app.buttons["welcome-continue"]
            XCTAssertTrue(next.waitForEnabled(timeout: 30), "Saving a location should enable Continue")
            attachScreenshot(app, "3-welcome-location")
            next.tap()
        }

        // Picks: one genre is enough to scan.
        if rock.waitForExistence(timeout: 60) {
            // The genres are the last section; scroll them clear of the
            // pinned footer, as a person would, before tapping one.
            app.swipeUp()
            rock.tap()
            let show = app.buttons["welcome-continue"]
            XCTAssertTrue(show.waitForEnabled(timeout: 30), "A genre pick should enable Show my concerts")
            attachScreenshot(app, "4-welcome-picks")
            show.tap()
        }

        // The feed: a cold genre scan takes a while, and the first-run screen
        // shows partial results on the way.
        XCTAssertTrue(event.waitForExistence(timeout: 300), "The feed should fill with shows")
        attachScreenshot(app, "5-feed")
        // Spotify must not be credited for a feed it had no part in.
        XCTAssertFalse(app.staticTexts["Powered by Spotify"].exists)
    }

    private func attachScreenshot(_ app: XCUIApplication, _ name: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }
}

private extension XCUIElement {
    func waitForEnabled(timeout: TimeInterval) -> Bool {
        let predicate = NSPredicate(format: "exists == true AND enabled == true")
        let expectation = XCTNSPredicateExpectation(predicate: predicate, object: self)
        return XCTWaiter().wait(for: [expectation], timeout: timeout) == .completed
    }
}
