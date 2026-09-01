import Foundation
import Testing

@testable import ConcertFinder

/// Rules that were introduced as prose in a view and pulled out so they can be
/// checked (plan P3-1, P3-4, P3-8).
///
/// Nothing here touches the network, the file system or `UserDefaults` — every
/// function under test takes its inputs as arguments. That is the point: the
/// state-owning versions of these rules live in the serialized suite in
/// `SessionLifecycleTests.swift`, because `UserDefaults` and
/// `StubURLProtocol.routes` are process-wide and two suites running in
/// parallel over them is a flake with no obvious cause.
struct FirstRunTests {

    // MARK: - P3-4: when to ask about notifications

    /// The prompt is asked once, at the moment it is earned. iOS shows the
    /// system dialog once and a decline is permanent from the app's side, so
    /// each of these three conditions is protecting the only chance there is.
    @Test func pushPromptIsOfferedOnlyWhenEarned() {
        // The one case that asks: a scan finished and it found them shows.
        #expect(FeedModel.shouldOfferPushPrompt(
            scanSettled: true, hasResults: true, alreadyOffered: false
        ))

        // Mid-scan. Asking someone to be notified about a feed they have not
        // yet seen work is the prompt people decline.
        #expect(!FeedModel.shouldOfferPushPrompt(
            scanSettled: false, hasResults: true, alreadyOffered: false
        ))

        // A finished scan that found nothing: nothing to be notified about,
        // and nothing demonstrating the app is worth a notification.
        #expect(!FeedModel.shouldOfferPushPrompt(
            scanSettled: true, hasResults: false, alreadyOffered: false
        ))

        // Once ever, whatever the answer was — the system dialog behind this
        // one cannot be re-raised from inside the app.
        #expect(!FeedModel.shouldOfferPushPrompt(
            scanSettled: true, hasResults: true, alreadyOffered: true
        ))
    }

    // MARK: - P3-8: the App Store link

    /// The button is hidden until there is a listing to open. The screen it
    /// sits on cannot be dismissed, so a button that opens the store to
    /// nothing is the last thing the user can do in the app.
    @Test func theAppStoreLinkExistsOnlyForARealAppID() {
        #expect(AppStoreLink.url(appID: "6740000000") != nil)
        #expect(AppStoreLink.url(appID: nil) == nil)
        #expect(AppStoreLink.url(appID: "") == nil)
        // What an unset build setting actually reaches the plist as. Without
        // the digit check this builds a URL that opens the store to nothing.
        #expect(AppStoreLink.url(appID: "$(CF_APP_STORE_APP_ID)") == nil)
    }

    // MARK: - P3-8: coarse coordinates

    /// The privacy manifest declares coarse location. `desiredAccuracy`
    /// bounds what CoreLocation spends to get a fix, not what it returns, so
    /// the rounding is what makes the declaration true of what leaves the
    /// device.
    @Test func coordinatesAreRoundedBeforeTheyLeaveTheDevice() {
        #expect(LocationModel.coarse(38.895037) == 38.9)
        #expect(LocationModel.coarse(-77.036543) == -77.04)
        // Already coarse values are unchanged, so re-saving a stored location
        // does not drift it.
        #expect(LocationModel.coarse(38.9) == 38.9)
    }
}
