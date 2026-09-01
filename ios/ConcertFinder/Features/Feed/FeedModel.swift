import Foundation
import Observation
import SwiftUI
import UIKit

/// Why a manual rescan was turned down.
///
/// The server distinguishes "you just refreshed" (15 minutes, per
/// `ManualRefreshMinInterval`) from "today's upstream allowance is gone"
/// (until the UTC day rolls over), and the remedies are different lengths of
/// wait. Carrying both means the UI can say which.
struct RescanRefusal: Equatable, Sendable {
    let until: Date?
    let reason: String?
}

/// State and behaviour for the concerts feed.
///
/// The polling rules here are the ones plan §5.3 calls out as worth designing
/// rather than discovering.
@MainActor
@Observable
final class FeedModel {
    private(set) var events: [Event] = []
    private(set) var facets: Facets = .empty
    private(set) var location: UserLocation?
    /// Whether the feed is being computed against the deployment's fallback
    /// city rather than one the user chose.
    ///
    /// This comes from `GET /me/location`, **not** from the feed response —
    /// the feed embeds a narrower location struct with no `is_default` field,
    /// so reading it there is false every time and the prompt never appears.
    /// One extra request on load, once.
    private(set) var isUsingFallbackLocation = false
    /// The point the feed is searched around, from `GET /me/location` — the
    /// deployment's fallback until the user names a city.
    ///
    /// Separate from `location` above, which the feed response overwrites
    /// with its own narrower struct. This one exists because the signed-out
    /// "popular shows near you" backdrop needs somewhere to search *before*
    /// the first feed read, which on a genuine first run has not happened.
    private(set) var searchOrigin: UserLocation?
    private(set) var computedAt: Date?
    private(set) var complete = true
    private(set) var retryAfter: Date?
    private(set) var isRefreshing = false
    private(set) var isLoading = false
    private(set) var error: APIError?
    /// True when what is on screen came from disk rather than the network.
    private(set) var isShowingCachedData = false
    /// A rescan is in flight. Distinct from `isRefreshing`, which is the
    /// server telling us a scan exists — this one is "the user just tapped".
    private(set) var isRescanning = false
    /// Why the last rescan was refused, when it was. A 429 here is an answer,
    /// not a failure, and it was previously stored and never rendered.
    private(set) var rescanRefusal: RescanRefusal?
    /// A notification named an event the feed does not contain. Landing on an
    /// unchanged feed with no explanation reads as a broken notification.
    private(set) var missingDeepLinkEvent = false
    /// The soft prompt for notifications is showing. Raised once ever, at the
    /// moment it is earned — see `offerPushPromptIfEarned`.
    var isShowingPushPrompt = false

    /// The feed's navigation stack, held here rather than in the view so a
    /// push notification can drive it. Views own their own paths only until
    /// something outside the view has to navigate.
    var path = NavigationPath()

    var filters: Filters {
        didSet {
            guard filters != oldValue else { return }
            FilterStore.save(filters)
            guard !isResetting else { return }
            Task { await load() }
        }
    }

    private let api: APIClient
    private var pollTask: Task<Void, Never>?
    private var pollCount = 0
    /// Guards the `filters` didSet during `reset()`. Clearing filters on
    /// sign-out would otherwise fire a fetch against a session that has just
    /// been thrown away.
    private var isResetting = false
    /// The user was asked where they are on first run and chose to go ahead
    /// without answering. Kept in memory only: it is an answer to one
    /// screen's question, not a preference, and the next launch reaching the
    /// same screen should ask again.
    private var didDeclineLocationStep = false

    /// The web client polls every 10s; matching it keeps the two clients'
    /// load profiles the same.
    private static let pollInterval: Duration = .seconds(10)

    /// A hard ceiling on polling — ~10 minutes. The web client bounds its
    /// loop and the app must too, or a scan that never completes turns into
    /// an indefinite timer.
    private static let maxPolls = 60

    init(api: APIClient) {
        self.api = api
        self.filters = FilterStore.load()
    }

    /// Events grouped into month sections, which is how the feed reads.
    var sections: [(key: String, heading: String, events: [Event])] {
        Self.monthSections(for: events)
    }

    /// Split out from `sections` and left free of model state so the ordering
    /// can be tested directly -- `events` is `private(set)`, so a test cannot
    /// stage one otherwise, and this got shipped backwards once.
    ///
    /// Sections are ordered by their earliest event rather than by `monthKey`,
    /// so the feed's order does not depend on the key's text format at all.
    nonisolated static func monthSections(
        for events: [Event]
    ) -> [(key: String, heading: String, events: [Event])] {
        Dictionary(grouping: events, by: { $0.date.monthKey })
            .map { key, items in
                let sorted = items.sorted { $0.date < $1.date }
                return (key: key, heading: sorted.first?.date.monthHeading ?? key, events: sorted)
            }
            .sorted {
                ($0.events.first?.date ?? .distantFuture)
                    < ($1.events.first?.date ?? .distantFuture)
            }
    }

    var isEmpty: Bool { events.isEmpty && !isLoading }

    /// Whether to show the first-run experience instead of the feed.
    ///
    /// Narrow on purpose. It is a genuine cold start — no snapshot has ever
    /// been served for this account — and either a scan is running or the
    /// flow has not yet asked where the user is. It must NOT fire for:
    ///
    /// - an established user whose feed is briefly empty (they have a
    ///   completed first run recorded);
    /// - someone in a quiet week with no upcoming shows (nothing is
    ///   refreshing, so there is nothing to wait for);
    /// - an offline launch (the cached snapshot renders instead).
    ///
    /// Getting this wrong the other way — showing "setting up" to a user who
    /// set up months ago — is worse than showing them an empty feed.
    var isFirstRun: Bool {
        guard !FirstRunTracker.hasCompleted else { return false }
        return isAwaitingLocation || isRefreshing || (isLoading && events.isEmpty)
    }

    /// The first-run flow is waiting on "where are you?" before it reads the
    /// feed at all.
    ///
    /// A feed read is what enqueues the scan now that login no longer
    /// pre-warms one, and a scan is up to five minutes and a chunk of a
    /// 250-call daily allowance. Spending that on the deployment's fallback
    /// city — which the user then replaces, producing a second `location_key`
    /// with no snapshot and a second full scan — means the user waits through
    /// a scan whose entire result is discarded. So on a genuine first run the
    /// question comes first and nothing is read until it is answered.
    ///
    /// Narrow deliberately. An established user who never set a location has
    /// `hasCompleted` and must still get their feed; a `/me/location` request
    /// that failed leaves `isUsingFallbackLocation` false, which loads, which
    /// is the safe direction.
    var isAwaitingLocation: Bool {
        guard !FirstRunTracker.hasCompleted else { return false }
        return isUsingFallbackLocation && !didDeclineLocationStep
    }

    /// "Search near a default city instead." The location step must not be a
    /// dead end for someone who declines location access and does not know
    /// what to type.
    func continueWithoutLocation() async {
        didDeclineLocationStep = true
        await load()
    }

    /// Records that the account has served a real feed at least once, so the
    /// first-run screen never returns.
    private func markFirstRunCompleteIfSettled(_ response: ConcertsResponse) {
        // "Settled" means a scan finished, not that it found anything: a user
        // whose area genuinely has no shows has still completed setup, and
        // showing them the waiting screen forever would be the worse failure.
        if !response.refreshing {
            FirstRunTracker.markCompleted()
        }
    }

    /// Whether this is the moment to ask about notifications.
    ///
    /// Static and pure so the rule can be tested without a scan: push is
    /// opt-in, defaults off, and a permission prompt is a one-shot — iOS
    /// shows the system dialog once and a decline is sticky. So it is asked
    /// exactly when it has been earned, which is when a scan has *finished*
    /// and actually found the user shows. Asking during the wait, or over an
    /// empty feed, is asking someone to be notified about something they have
    /// not yet seen work.
    nonisolated static func shouldOfferPushPrompt(
        scanSettled: Bool,
        hasResults: Bool,
        alreadyOffered: Bool
    ) -> Bool {
        scanSettled && hasResults && !alreadyOffered
    }

    private func offerPushPromptIfEarned(_ response: ConcertsResponse) {
        guard Self.shouldOfferPushPrompt(
            scanSettled: !response.refreshing,
            hasResults: !response.events.isEmpty,
            alreadyOffered: FirstRunTracker.hasOfferedPushPrompt
        ) else { return }
        // Recorded on *showing*, not on accepting. "Once ever" means the app
        // asks once and takes silence for an answer; re-raising it for a user
        // who dismissed it is the nagging the soft prompt exists to avoid.
        FirstRunTracker.markPushPromptOffered()
        isShowingPushPrompt = true
    }

    /// Renders the cached snapshot immediately so a cold launch shows content
    /// rather than a spinner, then goes to the network.
    func start() async {
        if events.isEmpty, let cached = await SnapshotCache.shared.load() {
            apply(cached)
        }
        // Before `load()`, not after. The feed read is what enqueues the
        // scan, so asking whether the user has a location of their own has to
        // happen while the answer can still stop one — see
        // `isAwaitingLocation`.
        await refreshLocationState()
        guard !isAwaitingLocation else { return }
        await load()
    }

    /// Asks the location endpoint whether the user has actually chosen one.
    /// Failure is silent: not knowing means not prompting, which is the safe
    /// direction — a spurious "set your location" banner over a location the
    /// user did set would be worse than no banner.
    func refreshLocationState() async {
        guard let current = try? await api.location() else { return }
        isUsingFallbackLocation = current.usesFallback
        searchOrigin = current
    }

    func load() async {
        isLoading = events.isEmpty
        do {
            let response = try await api.concerts(filters: filters)
            apply(response)
            await SnapshotCache.shared.store(response)
            markFirstRunCompleteIfSettled(response)
            offerPushPromptIfEarned(response)
            error = nil
            isShowingCachedData = false
            handlePollingState(refreshing: response.refreshing)
        } catch is CancellationError {
            // Switching tabs cancels the task this runs in. That is not a
            // failure to load anything: setting `error` here put "we couldn't
            // load your concerts" over a feed the user had simply navigated
            // away from, and stopped the poll that was tracking a live scan.
        } catch let apiError as APIError {
            // A transient failure over cached content is a banner, not an
            // error screen: replacing already-loaded data with an error is
            // strictly worse than showing it with a note.
            if apiError.isTransient, !events.isEmpty {
                isShowingCachedData = true
            }
            error = apiError
            stopPolling()
        } catch {
            self.error = .unknown(error.localizedDescription)
            stopPolling()
        }
        isLoading = false
    }

    /// Whether asking for a rescan can currently do anything.
    ///
    /// `retryAfter` outranks everything: it is the server's own statement that
    /// today's upstream allowance is spent, and a scan started before it lifts
    /// comes back capped by construction.
    var canRescan: Bool {
        guard !isRescanning, !isRefreshing else { return false }
        if let retryAfter = retryAfter, retryAfter > Date() { return false }
        return true
    }

    /// The explicit "search again" action.
    ///
    /// This is a real scan: it spends the user's daily Ticketmaster allowance
    /// and the server throttles it to one every 15 minutes. Pull-to-refresh
    /// deliberately does *not* come here — re-reading the snapshot is free,
    /// and it is what a pull actually means.
    func requestRescan() async {
        guard !isRescanning else { return }
        isRescanning = true
        rescanRefusal = nil
        defer { isRescanning = false }
        do {
            try await api.refreshConcerts()
            error = nil
            await load()
        } catch APIError.throttled(let until, let reason) {
            // Expected, not exceptional. Kept out of `error` so the feed does
            // not present a refusal to spend quota as a failure to load.
            retryAfter = until ?? retryAfter
            rescanRefusal = RescanRefusal(until: until, reason: reason)
        } catch is CancellationError {
            // Same reasoning as `load()`: a cancelled task is navigation, not
            // an error to report.
        } catch let apiError as APIError {
            error = apiError
        } catch {
            self.error = .unknown(error.localizedDescription)
        }
    }

    func dismissRescanRefusal() { rescanRefusal = nil }

    // MARK: - Deep links

    /// Resolves the event key a notification carried and pushes its card.
    ///
    /// The payload names a key and nothing else — APNs caps at 4KB — so it has
    /// to be matched against the feed. A key missing from the loaded feed is
    /// not yet proof of anything: the notification exists precisely because a
    /// show was announced since the last fetch, so the absence is fetched
    /// against once before it is believed.
    func openEvent(withKey key: String) async {
        missingDeepLinkEvent = false
        if push(eventWithKey: key) { return }
        await load()
        if push(eventWithKey: key) { return }
        // Filters are the other reason a key can be absent, and clearing
        // someone's filters to reveal a card would be a surprising thing to do
        // to their screen. Say so instead.
        missingDeepLinkEvent = true
    }

    func dismissMissingDeepLink() { missingDeepLinkEvent = false }

    private func push(eventWithKey key: String) -> Bool {
        guard let event = events.first(where: { $0.eventKey == key }) else { return false }
        path.append(event)
        return true
    }

    // MARK: - Sign-out

    /// Drops everything belonging to the signed-out account.
    ///
    /// Every field, not the obvious ones: a surviving `computedAt` puts
    /// "updated 3h ago" over the next account's cold feed, and a surviving
    /// poll task keeps fetching against a token that no longer exists.
    func reset() {
        stopPolling()
        pollCount = 0
        path = NavigationPath()
        events = []
        facets = .empty
        location = nil
        isUsingFallbackLocation = false
        searchOrigin = nil
        computedAt = nil
        complete = true
        retryAfter = nil
        isRefreshing = false
        isLoading = false
        isRescanning = false
        rescanRefusal = nil
        missingDeepLinkEvent = false
        isShowingPushPrompt = false
        didDeclineLocationStep = false
        error = nil
        isShowingCachedData = false
        isResetting = true
        filters = .empty
        isResetting = false
        FilterStore.clear()
    }

    private func apply(_ response: ConcertsResponse) {
        events = response.events
        facets = response.facets
        location = response.location
        computedAt = response.computedAt
        complete = response.complete
        retryAfter = response.retryAfter
        isRefreshing = response.refreshing
    }

    private func apply(_ entry: SnapshotCache.Entry) {
        // A snapshot on disk is proof the account has been through setup, so
        // an offline cold launch shows the cached feed rather than "setting
        // up" over content we already have.
        FirstRunTracker.markCompleted()
        events = entry.events
        facets = entry.facets
        location = entry.location
        computedAt = entry.computedAt
        complete = entry.complete
        isShowingCachedData = true
    }

    // MARK: - Polling

    private func handlePollingState(refreshing: Bool) {
        if refreshing {
            startPollingIfNeeded()
        } else {
            stopPolling()
        }
    }

    private func startPollingIfNeeded() {
        guard pollTask == nil else { return }
        pollCount = 0
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: Self.pollInterval)
                guard let self, !Task.isCancelled else { return }
                let shouldContinue = await self.pollOnce()
                if !shouldContinue { return }
            }
        }
    }

    /// One poll. Returns whether to keep going.
    private func pollOnce() async -> Bool {
        pollCount += 1
        guard pollCount <= Self.maxPolls else {
            stopPolling()
            return false
        }
        do {
            let response = try await api.concerts(filters: filters)
            apply(response)
            await SnapshotCache.shared.store(response)
            markFirstRunCompleteIfSettled(response)
            offerPushPromptIfEarned(response)
            if !response.refreshing {
                stopPolling()
                return false
            }
            return true
        } catch {
            // A transient error must not kill the loop or replace loaded
            // data with an error screen — the scan is probably still running.
            if let apiError = error as? APIError, apiError.isTransient {
                return true
            }
            stopPolling()
            return false
        }
    }

    private func stopPolling() {
        pollTask?.cancel()
        pollTask = nil
        isRefreshing = false
    }

    /// A 10-second timer that survives backgrounding is a battery complaint
    /// and an App Review question. The scene calls these on transition.
    func suspendPolling() {
        pollTask?.cancel()
        pollTask = nil
    }

    func resumePollingIfNeeded() {
        guard isRefreshing else { return }
        startPollingIfNeeded()
    }

    // MARK: - Per-act mutations

    /// Save and unsave are per act, and the change is applied optimistically
    /// so the bookmark responds immediately. On failure the state is put back
    /// — a control that silently lies about what it did is worse than a slow
    /// one.
    func toggleSave(act: Act) async {
        let target = !act.isSaved
        setSaved(target, for: act.dedupKey)
        do {
            if target {
                try await api.save(dedupKey: act.dedupKey)
            } else {
                try await api.unsave(dedupKey: act.dedupKey)
            }
        } catch {
            setSaved(!target, for: act.dedupKey)
            self.error = error as? APIError ?? .unknown(error.localizedDescription)
        }
    }

    /// Subscribing patches the artist across *every* event in the list: one
    /// artist can appear on several bills, and leaving the others stale makes
    /// the bell look broken.
    func toggleSubscribe(act: Act) async {
        let target = !act.isSubscribed
        setSubscribed(target, forArtist: act.artist.id)
        do {
            if target {
                try await api.subscribe(artistID: act.artist.id)
            } else {
                try await api.unsubscribe(artistID: act.artist.id)
            }
        } catch {
            setSubscribed(!target, forArtist: act.artist.id)
            self.error = error as? APIError ?? .unknown(error.localizedDescription)
        }
    }

    private func setSaved(_ saved: Bool, for dedupKey: String) {
        for i in events.indices {
            for j in events[i].acts.indices where events[i].acts[j].dedupKey == dedupKey {
                events[i].acts[j].saved = saved
            }
        }
    }

    private func setSubscribed(_ subscribed: Bool, forArtist artistID: String) {
        for i in events.indices {
            for j in events[i].acts.indices where events[i].acts[j].artist.id == artistID {
                events[i].acts[j].subscribed = subscribed
            }
        }
    }

    func clearError() { error = nil }
}
