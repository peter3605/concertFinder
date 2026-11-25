import XCTest

/// App Store screenshots (CF-19), captured against production with the App
/// Review account, so every release can retake them the same way.
///
/// Production because the Apple button appears only when the server's
/// site-info reports `apple_sign_in`, and a local stack has no `APPLE_*` set.
/// The review account because it is already seeded with a city and genres, so
/// the feed is full and no onboarding writes anything.
///
///     xcrun simctl status_bar <device> override --time 9:41 \
///       --batteryState charged --batteryLevel 100 --cellularBars 4 --wifiBars 3
///     TEST_RUNNER_CF_UI_TEST_API_BASE_URL=https://concertfinder.app \
///     TEST_RUNNER_CF_UI_TEST_SHOT_EMAIL=<SSM /concertfinder/REVIEW_LOGIN_EMAIL> \
///     TEST_RUNNER_CF_UI_TEST_SHOT_CODE=<SSM /concertfinder/REVIEW_LOGIN_CODE> \
///       xcodebuild test -only-testing:ConcertFinderUITests/ScreenshotUITests \
///       -parallel-testing-enabled NO CODE_SIGN_IDENTITY=- ...
///
/// Then pull the attachments out of the .xcresult with
/// `xcrun xcresulttool export attachments`.
///
/// It must leave the review account exactly as it found it, because App
/// Review signs into the same one: it saves nothing, subscribes to nothing,
/// and opens the picks editor without adding or removing a pick. Each run
/// that reaches the sign-in screen spends one of the address's three code
/// requests per hour; a run that finds a session already in the Keychain
/// spends none.
///
/// Without the variables it skips, so CI never signs into production.
final class ScreenshotUITests: XCTestCase {

    private var env: [String: String] { ProcessInfo.processInfo.environment }

    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    @MainActor
    func testCaptureStoreScreenshots() throws {
        guard let base = env["CF_UI_TEST_API_BASE_URL"], !base.isEmpty,
              let email = env["CF_UI_TEST_SHOT_EMAIL"], !email.isEmpty,
              let code = env["CF_UI_TEST_SHOT_CODE"], !code.isEmpty else {
            throw XCTSkip("Set CF_UI_TEST_API_BASE_URL, CF_UI_TEST_SHOT_EMAIL and CF_UI_TEST_SHOT_CODE to capture screenshots")
        }
        let app = XCUIApplication()
        app.launchEnvironment["CF_UI_TEST_API_BASE_URL"] = base
        // No push-permission sheet and no one-off hint over the feed.
        app.launchArguments += ["-cf.pushPromptOffered", "YES", "-cf.hint.saveVersusSubscribe", "YES"]
        app.launch()

        let event = app.descendants(matching: .any)["feed-event"].firstMatch
        let emailField = app.textFields["email-address"]

        // Signed out: the sign-in choices, then the code step.
        if emailField.waitForExistence(timeout: 20) {
            XCTAssertTrue(app.buttons["sign-in-apple"].waitForExistence(timeout: 10),
                          "The Apple button shows only when the server reports apple_sign_in")
            attachScreenshot(app, "01-sign-in")

            emailField.tap()
            emailField.typeText(email)
            app.buttons["email-send"].tap()
            let codeField = app.textFields["email-code"]
            XCTAssertTrue(codeField.waitForExistence(timeout: 30), "The code step should show")
            // No shot of this step: it names the review address, and once
            // typed, shows the review code. Screenshots are public.
            codeField.tap()
            codeField.typeText(code)
            app.buttons["email-code-submit"].tap()
        }

        // The feed. Wait for the scan to settle, so the shot is not a spinner.
        XCTAssertTrue(event.waitForExistence(timeout: 120), "The review account's feed should be populated")
        sleep(3)
        attachScreenshot(app, "02-feed")

        // One concert, opened. Viewing it changes nothing.
        event.tap()
        XCTAssertTrue(app.buttons["Add to Calendar"].waitForExistence(timeout: 20)
                      || app.staticTexts["Tickets"].waitForExistence(timeout: 5))
        sleep(1)
        attachScreenshot(app, "03-event")

        // The picks flow: Settings → Artists and genres, and an artist search.
        // Nothing is added: the review account must stay as seeded.
        openSection(app, "Settings")
        let picksLink = app.buttons["Artists and genres"].firstMatch
        XCTAssertTrue(picksLink.waitForExistence(timeout: 20))
        picksLink.tap()
        XCTAssertTrue(app.navigationBars["Artists and genres"].waitForExistence(timeout: 20)
                      || app.staticTexts["Artists and genres"].waitForExistence(timeout: 5))
        sleep(2)
        attachScreenshot(app, "04-picks")

        let search = app.textFields["picks-search"]
        if search.waitForExistence(timeout: 10) {
            search.tap()
            search.typeText("Phoebe Bridgers")
            if app.descendants(matching: .any)["pick-result"].firstMatch.waitForExistence(timeout: 30) {
                // Dismiss the keyboard so the results are what the shot shows.
                for key in ["search", "Search", "return", "Return"] where app.keyboards.buttons[key].exists {
                    app.keyboards.buttons[key].tap()
                    break
                }
                sleep(1)
                attachScreenshot(app, "05-picks-search")
            }
        }
    }

    /// A tab on iPhone, a sidebar row on iPad.
    private func openSection(_ app: XCUIApplication, _ title: String) {
        let candidates = [
            app.tabBars.buttons[title],
            app.buttons[title],
            app.cells.containing(.staticText, identifier: title).firstMatch,
            app.staticTexts[title],
        ]
        for element in candidates where element.waitForExistence(timeout: 3) {
            element.tap()
            return
        }
        XCTFail("No tab or sidebar row named \(title)")
    }

    private func attachScreenshot(_ app: XCUIApplication, _ name: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }
}
