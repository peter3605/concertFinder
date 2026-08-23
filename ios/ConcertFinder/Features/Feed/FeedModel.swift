import Foundation
import Observation
import UIKit

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
    private(set) var computedAt: Date?
    private(set) var complete = true
    private(set) var retryAfter: Date?
    private(set) var isRefreshing = false
    private(set) var isLoading = false
    private(set) var error: APIError?
    /// True when what is on screen came from disk rather than the network.
    private(set) var isShowingCachedData = false

    var filters: Filters {
        didSet {
            guard filters != oldValue else { return }
            FilterStore.save(filters)
            Task { await load() }
        }
    }

    private let api: APIClient
    private var pollTask: Task<Void, Never>?
    private var pollCount = 0

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
        let grouped = Dictionary(grouping: events, by: { $0.date.monthKey })
        return grouped.keys.sorted().map { key in
            let items = (grouped[key] ?? []).sorted { $0.date < $1.date }
            return (key: key, heading: items.first?.date.monthHeading ?? key, events: items)
        }
    }

    var isEmpty: Bool { events.isEmpty && !isLoading }

    /// Renders the cached snapshot immediately so a cold launch shows content
    /// rather than a spinner, then goes to the network.
    func start() async {
        if events.isEmpty, let cached = await SnapshotCache.shared.load() {
            apply(cached)
        }
        await load()
    }

    func load() async {
        isLoading = events.isEmpty
        do {
            let response = try await api.concerts(filters: filters)
            apply(response)
            await SnapshotCache.shared.store(response)
            error = nil
            isShowingCachedData = false
            handlePollingState(refreshing: response.refreshing)
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

    /// Pull-to-refresh and the manual refresh button.
    func refresh() async {
        do {
            try await api.refreshConcerts()
            await load()
        } catch let apiError as APIError {
            // A 429 here is expected, not exceptional — it carries when the
            // throttle lifts and why, and both belong on screen.
            if case .throttled(let retry, _) = apiError {
                retryAfter = retry ?? retryAfter
            }
            error = apiError
        } catch {
            self.error = .unknown(error.localizedDescription)
        }
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
