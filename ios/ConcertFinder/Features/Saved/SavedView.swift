import Observation
import SwiftUI

@MainActor
@Observable
final class SavedModel {
    private(set) var events: [Event] = []
    private(set) var isLoading = false
    private(set) var error: APIError?

    private let api: APIClient

    init(api: APIClient) {
        self.api = api
    }

    /// Past shows are already floored out server-side, at the start of the
    /// current UTC day — the same rule the feed uses. Nothing to filter here.
    func load() async {
        isLoading = events.isEmpty
        do {
            events = try await api.savedConcerts().events
            error = nil
        } catch let apiError as APIError {
            error = apiError
        } catch {
            self.error = .unknown(error.localizedDescription)
        }
        isLoading = false
    }

    /// Save and unsave are per act — by dedup_key, not per card — and the
    /// change is applied optimistically, the same contract `FeedModel` offers
    /// so that `EventDetailView` behaves identically whichever list pushed it.
    ///
    /// The tap used to *remove* the act instead of clearing its flag, and
    /// removal cannot drive a detail screen: unsaving from one would delete
    /// the row under the user's finger and, on a single-act show, the event
    /// with it — leaving the screen to fall back to the copy the list pushed,
    /// whose bookmark is still filled. So both surfaces now mean one thing by
    /// the gesture, and a card the unsave empties is dropped by the next
    /// `load()` rather than mid-gesture. The row that lingers until then reads
    /// "Not saved" and can be tapped again, which is also how an accidental
    /// unsave gets undone; removal offered no way back short of finding the
    /// show again in the feed.
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

    /// Subscribing patches the artist across every event here, for the reason
    /// `FeedModel` does it: one artist can appear on several bills, and
    /// leaving the others stale makes the bell look broken.
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

    /// Sign-out. Saves belong to an account, and the next one on this device
    /// must not open the tab onto someone else's.
    func reset() {
        events = []
        isLoading = false
        error = nil
    }
}

/// Declared, not just satisfied by coincidence: `EventDetailView` resolves one
/// of these per event and both reads and writes through it, so the two models
/// have to be substitutable rather than merely similar.
extension SavedModel: EventStore {}

struct SavedView: View {
    @Environment(SavedModel.self) private var model

    var body: some View {
        NavigationStack {
            Group {
                if model.isLoading && model.events.isEmpty {
                    ProgressView()
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if model.events.isEmpty {
                    emptyState
                } else {
                    list
                }
            }
            .background(Color.screenBackground)
            .navigationTitle("Saved")
            .refreshable { await model.load() }
            .task { await model.load() }
            .navigationDestination(for: Event.self) { EventDetailView(event: $0) }
        }
    }

    /// A failed load and an account with no saves both produce an empty list.
    /// Telling the second story over the first says the user's saves are gone.
    private var emptyState: some View {
        ContentUnavailableView {
            Label(model.error == nil ? "Nothing saved" : "We couldn't load your saved shows",
                  systemImage: model.error == nil ? "bookmark" : "exclamationmark.triangle")
        } description: {
            if let error = model.error {
                Text(error.userMessage)
            } else {
                Text("Tap the bookmark on any show to keep it here.")
            }
        } actions: {
            // The view does not scroll, so a pull gesture is not available
            // here at all — the retry has to be a button.
            if model.error != nil {
                Button("Try again") { Task { await model.load() } }
            }
        }
    }

    private var list: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: Metrics.cardSpacing) {
                // An unsave that failed rolls the row back; without this the
                // rollback is the only sign anything went wrong.
                if let error = model.error {
                    InfoBanner(kind: .error(error.userMessage))
                }
                ForEach(model.events) { event in
                    NavigationLink(value: event) {
                        SavedCard(event: event) { act in
                            Task { await model.toggleSave(act: act) }
                        }
                    }
                    .buttonStyle(.plain)
                }
                SpotifyAttribution()
                    .frame(maxWidth: .infinity, alignment: .center)
                    .padding(.top, Metrics.loose)
            }
            .padding(Metrics.gutter)
        }
    }
}

private struct SavedCard: View {
    let event: Event
    var onToggleSave: (Act) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: Metrics.tight) {
            VStack(alignment: .leading, spacing: 2) {
                Text(event.venue).font(.headline)
                Text(event.location).font(.subheadline).foregroundStyle(.secondary)
                Text(event.date.formatted(date: .abbreviated, time: .shortened))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Divider()
            ForEach(event.acts) { act in
                HStack {
                    Text(act.artist.name).font(.subheadline.weight(.medium))
                    Spacer()
                    // Rendered from the act's own flag and labelled exactly as
                    // `ActRow` does it, because the same act is reachable
                    // through both: a hardcoded filled bookmark here would
                    // contradict the detail screen the moment an unsave there
                    // cleared the flag, and would go on telling VoiceOver
                    // "Saved" about a show that is not.
                    Button {
                        onToggleSave(act)
                    } label: {
                        Image(systemName: act.isSaved ? "bookmark.fill" : "bookmark")
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(act.isSaved ? Color.accentColor : Color.secondary)
                    .accessibilityLabel("Save \(act.artist.name)")
                    .accessibilityValue(act.isSaved ? "Saved" : "Not saved")
                    .accessibilityAddTraits(act.isSaved ? [.isButton, .isSelected] : .isButton)
                }
                .contentShape(Rectangle())
            }
        }
        .padding(Metrics.gutter)
        .background(Color.cardBackground)
        .clipShape(RoundedRectangle(cornerRadius: Metrics.cardRadius, style: .continuous))
    }
}
